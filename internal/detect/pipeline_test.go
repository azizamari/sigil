package detect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azizamari/sigil/internal/codebook"
	"github.com/azizamari/sigil/internal/embed"
	"github.com/azizamari/sigil/internal/storage"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("needs ffmpeg; this is an L2 test")
	}
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
}

func ffmpegRun(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v: %s", args, err, out)
	}
}

// leakFor builds the file a viewer would walk away with: the segments their
// session was actually served, concatenated back into one video.
func leakFor(t *testing.T, dir string, variantDirs map[uint8]string, sequence []uint8, count int) string {
	t.Helper()
	list := filepath.Join(dir, "concat.txt")
	var b strings.Builder
	for i := range count {
		seg := filepath.Join(variantDirs[sequence[i]], "seg_"+pad(i)+".ts")
		if _, err := os.Stat(seg); err != nil {
			t.Fatalf("missing packaged segment %s", seg)
		}
		b.WriteString("file '" + seg + "'\n")
	}
	if err := os.WriteFile(list, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	leak := filepath.Join(dir, "leak.mp4")
	ffmpegRun(t, "-f", "concat", "-safe", "0", "-i", list, "-c", "copy", leak)
	return leak
}

func pad(i int) string {
	s := "00000" + itoa(i)
	return s[len(s)-5:]
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// packFixture encodes both variants of a short source at a segment duration
// small enough that a usable codeword fits.
func packFixture(t *testing.T, dir string, seconds int, segmentSeconds float64) (storage.Meta, map[uint8]string) {
	t.Helper()
	src := filepath.Join(dir, "src.mp4")
	ffmpegRun(t, "-f", "lavfi", "-i", "testsrc2=size=960x540:rate=15:duration="+itoa(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-pix_fmt", "yuv420p", src)

	ep := embed.DefaultParams(960, 540)
	ep.Amplitude = 10
	f := embed.NewFFmpeg()
	f.Encode = []string{"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-pix_fmt", "yuv420p"}

	dirs := map[uint8]string{}
	count := 0
	for _, variant := range []uint8{0, 1} {
		out := filepath.Join(dir, "v"+itoa(int(variant)))
		if err := f.EmbedHLS(context.Background(), src, out, variant, ep, segmentSeconds); err != nil {
			t.Fatalf("EmbedHLS: %v", err)
		}
		segs, err := filepath.Glob(filepath.Join(out, "seg_*.ts"))
		if err != nil {
			t.Fatal(err)
		}
		dirs[variant] = out
		count = len(segs)
	}

	cb, err := codebook.Fit(3, count, count)
	if err != nil {
		t.Fatalf("no codebook fits %d segments: %v", count, err)
	}
	cb.SegmentCount = count
	meta := storage.Meta{
		Version:         storage.MetaVersion,
		AssetID:         "lecture-01",
		SegmentCount:    count,
		SegmentDuration: segmentSeconds,
		TotalDuration:   float64(seconds),
		Watermarked:     true,
		Codebook:        &cb,
		Embed:           &ep,
	}
	if err := meta.Validate(); err != nil {
		t.Fatalf("fixture meta: %v", err)
	}
	return meta, dirs
}

// The whole project in one test: package, hand a session its variants, and
// recover the session from the file it walked away with.
func TestRunAttributesARealLeak(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	meta, dirs := packFixture(t, dir, 16, 0.5)

	book, err := codebook.New(*meta.Codebook)
	if err != nil {
		t.Fatal(err)
	}
	const payload = 5
	sequence, err := book.Sequence(payload)
	if err != nil {
		t.Fatal(err)
	}
	leak := leakFor(t, dir, dirs, sequence, meta.SegmentCount)

	issued := []Issued{
		{SessionID: "ses_other", PayloadID: 1},
		{SessionID: "ses_guilty", PayloadID: payload},
		{SessionID: "ses_third", PayloadID: 6},
	}
	d := &Detector{Extractor: embed.NewFFmpeg(), Threshold: 0.5}
	res, err := d.Run(context.Background(), leak, meta, issued)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Logf("session=%s confidence=%.3f null=%.3f bits=%d/%d offset=%d",
		res.SessionID, res.Confidence, res.NullPeak, res.BitsRecovered, res.BitsTotal, res.Offset)
	if res.SessionID != "ses_guilty" {
		t.Errorf("attributed to %q, want ses_guilty", res.SessionID)
	}
	if !res.Matched {
		t.Errorf("a clean leak was not matched (confidence %.3f, null %.3f)", res.Confidence, res.NullPeak)
	}
}

// SPEC 10: unwatermarked content must produce no attribution. This is the test
// that stops the detector being an accusation generator.
func TestRunOnUnwatermarkedContentDoesNotAttribute(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	meta, _ := packFixture(t, dir, 16, 0.5)

	// Keyframes must land on the segment grid or the file will not split, and
	// the test would pass without ever reaching the matcher.
	clean := filepath.Join(dir, "clean.mp4")
	ffmpegRun(t, "-f", "lavfi", "-i", "testsrc2=size=960x540:rate=15:duration=16",
		"-force_key_frames", "expr:gte(t,n_forced*0.5)", "-sc_threshold", "0",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-pix_fmt", "yuv420p", clean)

	issued := []Issued{
		{SessionID: "ses_a", PayloadID: 1},
		{SessionID: "ses_b", PayloadID: 5},
	}
	d := &Detector{Extractor: embed.NewFFmpeg(), Threshold: DefaultThreshold}
	res, err := d.Run(context.Background(), clean, meta, issued)
	if err != nil {
		t.Fatalf("unwatermarked content must reach the matcher and be rejected, not fail to split: %v", err)
	}
	t.Logf("unwatermarked: session=%q matched=%v confidence=%.3f null=%.3f",
		res.SessionID, res.Matched, res.Confidence, res.NullPeak)
	if res.Matched {
		t.Errorf("unwatermarked content was attributed to %q at confidence %.3f",
			res.SessionID, res.Confidence)
	}
}

func TestRunRequiresAWatermarkedAsset(t *testing.T) {
	d := &Detector{Extractor: embed.NewFFmpeg()}
	if _, err := d.Run(context.Background(), "leak.mp4", storage.Meta{}, nil); err == nil {
		t.Fatal("Run on an unwatermarked asset = nil error, want error")
	}
	empty := &Detector{}
	if _, err := empty.Run(context.Background(), "leak.mp4", storage.Meta{}, nil); err == nil {
		t.Fatal("Run without an extractor = nil error, want error")
	}
}
