package embed

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// These need a real encoder but no containers, so they run wherever ffmpeg is
// on PATH rather than hiding behind the integration tag.
func requireFFmpeg(t *testing.T) *FFmpeg {
	t.Helper()
	if testing.Short() {
		t.Skip("needs ffmpeg; this is an L2 test")
	}
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	f := NewFFmpeg()
	f.Encode = []string{"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-pix_fmt", "yuv420p"}
	return f
}

func synthSource(t *testing.T, f *FFmpeg, dir string, seconds int) string {
	t.Helper()
	path := filepath.Join(dir, "src.mp4")
	cmd := exec.Command(f.bin(), "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=960x540:rate=15:duration="+itoa(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-pix_fmt", "yuv420p", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, out)
	}
	return path
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

// The whole scheme rests on this: two encodes of the same source must come back
// with opposite signs through a real codec.
func TestEmbedExtractRoundTripThroughFFmpeg(t *testing.T) {
	f := requireFFmpeg(t)
	dir := t.TempDir()
	src := synthSource(t, f, dir, 2)

	p := DefaultParams(960, 540)

	soft := map[uint8]float64{}
	for _, variant := range []uint8{0, 1} {
		dst := filepath.Join(dir, "v"+itoa(int(variant))+".mp4")
		if err := f.Embed(context.Background(), src, dst, variant, p); err != nil {
			t.Fatalf("Embed variant %d: %v", variant, err)
		}
		got, err := f.Extract(context.Background(), dst, p)
		if err != nil {
			t.Fatalf("Extract variant %d: %v", variant, err)
		}
		soft[variant] = got
	}

	if soft[0] >= 0 {
		t.Errorf("variant 0 extracted %+.3f, want negative", soft[0])
	}
	if soft[1] <= 0 {
		t.Errorf("variant 1 extracted %+.3f, want positive", soft[1])
	}
	t.Logf("soft decisions: v0 %+.3f, v1 %+.3f", soft[0], soft[1])
}

// SPEC 10: detection must be run against unwatermarked content and return no
// match, or the detector names someone for every input it is given.
func TestUnwatermarkedContentExtractsWeakly(t *testing.T) {
	f := requireFFmpeg(t)
	dir := t.TempDir()
	src := synthSource(t, f, dir, 2)

	p := DefaultParams(960, 540)

	marked := filepath.Join(dir, "marked.mp4")
	if err := f.Embed(context.Background(), src, marked, 0, p); err != nil {
		t.Fatal(err)
	}
	signal, err := f.Extract(context.Background(), marked, p)
	if err != nil {
		t.Fatal(err)
	}
	null, err := f.Extract(context.Background(), src, p)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("watermarked %+.3f, unwatermarked %+.3f", signal, null)
	if absf(null) >= absf(signal) {
		t.Errorf("unwatermarked content scored %+.3f against a real mark of %+.3f", null, signal)
	}
}

func TestEmbedRejectsBadParams(t *testing.T) {
	f := requireFFmpeg(t)
	p := DefaultParams(960, 540)
	p.Amplitude = 0
	if err := f.Embed(context.Background(), "missing.mp4", "out.mp4", 0, p); err == nil {
		t.Error("Embed with invalid params = nil error, want error")
	}
	p = DefaultParams(640, 360)
	if err := f.Embed(context.Background(), "missing.mp4", filepath.Join(t.TempDir(), "o.mp4"), 2, p); err == nil {
		t.Error("Embed with variant 2 = nil error, want error")
	}
}

// Both variants must split identically or a session's sequence would drift the
// moment the player switched branches.
func TestBothVariantsSegmentIdentically(t *testing.T) {
	f := requireFFmpeg(t)
	dir := t.TempDir()
	src := synthSource(t, f, dir, 6)
	p := DefaultParams(960, 540)

	counts := map[uint8]int{}
	for _, variant := range []uint8{0, 1} {
		out := filepath.Join(dir, "v"+itoa(int(variant)))
		if err := f.EmbedHLS(context.Background(), src, out, variant, p, 1); err != nil {
			t.Fatalf("EmbedHLS variant %d: %v", variant, err)
		}
		entries, err := filepath.Glob(filepath.Join(out, "seg_*.ts"))
		if err != nil {
			t.Fatal(err)
		}
		counts[variant] = len(entries)
		if len(entries) < 4 {
			t.Fatalf("variant %d produced %d segments, want at least 4", variant, len(entries))
		}
		if _, err := os.Stat(filepath.Join(out, "index.m3u8")); err != nil {
			t.Errorf("variant %d has no playlist: %v", variant, err)
		}
	}
	if counts[0] != counts[1] {
		t.Errorf("variants split differently: %d vs %d segments", counts[0], counts[1])
	}
}

// Per-segment extraction is what feeds the sequence decoder, so each segment
// must carry a readable bit on its own.
func TestSegmentsCarryTheirVariantIndividually(t *testing.T) {
	f := requireFFmpeg(t)
	dir := t.TempDir()
	src := synthSource(t, f, dir, 6)
	p := DefaultParams(960, 540)
	p.Amplitude = 8

	for _, variant := range []uint8{0, 1} {
		out := filepath.Join(dir, "v"+itoa(int(variant)))
		if err := f.EmbedHLS(context.Background(), src, out, variant, p, 1); err != nil {
			t.Fatal(err)
		}
		segments, err := filepath.Glob(filepath.Join(out, "seg_*.ts"))
		if err != nil {
			t.Fatal(err)
		}

		var correct int
		for _, seg := range segments {
			soft, err := f.Extract(context.Background(), seg, p)
			if err != nil {
				t.Fatalf("extract %s: %v", seg, err)
			}
			if (soft > 0) == (variant == 1) {
				correct++
			}
		}
		if correct != len(segments) {
			t.Errorf("variant %d: %d/%d segments read back correctly", variant, correct, len(segments))
		}
	}
}

func TestDurationAndSize(t *testing.T) {
	f := requireFFmpeg(t)
	dir := t.TempDir()
	src := synthSource(t, f, dir, 3)

	w, h, err := f.Size(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if w != 960 || h != 540 {
		t.Errorf("Size = %dx%d, want 960x540", w, h)
	}
	d, err := f.Duration(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if d < 2.5 || d > 3.5 {
		t.Errorf("Duration = %.2f, want about 3", d)
	}
	if _, _, err := f.Size(context.Background(), filepath.Join(dir, "nope.mp4")); err == nil {
		t.Error("Size of a missing file = nil error, want error")
	}
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
