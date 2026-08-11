package manifest

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azizamari/sigil/internal/codebook"
	"github.com/azizamari/sigil/internal/storage"
)

// fakeSigner keeps playlists deterministic so golden files stay stable; the
// real signature is exercised against MinIO at L2.
type fakeSigner struct {
	calls []string
	ttls  []time.Duration
	err   error
}

func (f *fakeSigner) Sign(_ context.Context, key string, ttl time.Duration) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.calls = append(f.calls, key)
	f.ttls = append(f.ttls, ttl)
	return "https://cdn.example.com/" + key + "?sig=" + strconv.Itoa(len(f.calls)), nil
}

func watermarkedMeta(segments int) storage.Meta {
	return storage.Meta{
		Version:         storage.MetaVersion,
		AssetID:         "lecture-01",
		SegmentCount:    segments,
		SegmentDuration: 4,
		TotalDuration:   float64(segments) * 4,
		Watermarked:     true,
		Codebook:        &codebook.Params{Version: codebook.Version, M: 5, T: 3, SegmentCount: segments},
	}
}

func build(t *testing.T, meta storage.Meta, seq []uint8, opts Options) (string, *fakeSigner) {
	t.Helper()
	fs := &fakeSigner{}
	b, err := NewBuilder(fs)
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}
	if opts.SegmentTTL == 0 {
		opts.SegmentTTL = time.Hour
	}
	out, err := b.Build(context.Background(), meta, seq, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return out, fs
}

func TestNewBuilderRejectsNilSigner(t *testing.T) {
	if _, err := NewBuilder(nil); err == nil {
		t.Fatal("NewBuilder(nil) = nil error, want error")
	}
}

// The variant chosen per segment is the entire watermark, so an interleaving
// bug silently attributes leaks to the wrong session.
func TestVariantInterleavingMatchesSequence(t *testing.T) {
	seq := []uint8{1, 0, 0, 1, 1, 0, 1, 0}
	meta := watermarkedMeta(len(seq))
	_, fs := build(t, meta, seq, Options{})

	if len(fs.calls) != len(seq) {
		t.Fatalf("signed %d keys, want %d", len(fs.calls), len(seq))
	}
	for i, bit := range seq {
		want := fmt.Sprintf("assets/lecture-01/v%d/seg_%05d.ts", bit, i+1)
		if fs.calls[i] != want {
			t.Errorf("segment %d signed %q, want %q", i+1, fs.calls[i], want)
		}
	}
}

func TestUnwatermarkedAssetUsesFlatKeys(t *testing.T) {
	meta := watermarkedMeta(3)
	meta.Watermarked = false
	meta.Codebook = nil
	_, fs := build(t, meta, nil, Options{})

	for i, got := range fs.calls {
		want := fmt.Sprintf("assets/lecture-01/seg_%05d.ts", i+1)
		if got != want {
			t.Errorf("segment %d signed %q, want %q", i+1, got, want)
		}
	}
}

func TestBuildRejectsShortSequence(t *testing.T) {
	fs := &fakeSigner{}
	b, _ := NewBuilder(fs)
	_, err := b.Build(context.Background(), watermarkedMeta(10), []uint8{1, 0}, Options{SegmentTTL: time.Hour})
	if err == nil {
		t.Fatal("Build with a short sequence = nil error, want error")
	}
}

func TestBuildRejectsBadOptions(t *testing.T) {
	fs := &fakeSigner{}
	b, _ := NewBuilder(fs)
	seq := make([]uint8, 4)
	if _, err := b.Build(context.Background(), watermarkedMeta(4), seq, Options{}); err == nil {
		t.Error("Build with zero ttl = nil error, want error")
	}
	if _, err := b.Build(context.Background(), storage.Meta{}, seq, Options{SegmentTTL: time.Hour}); err == nil {
		t.Error("Build with invalid meta = nil error, want error")
	}
}

func TestSegmentTTLIsApplied(t *testing.T) {
	seq := []uint8{0, 1, 0}
	_, fs := build(t, watermarkedMeta(3), seq, Options{SegmentTTL: 90 * time.Minute})
	for i, ttl := range fs.ttls {
		if ttl != 90*time.Minute {
			t.Errorf("segment %d signed with ttl %s, want 90m", i+1, ttl)
		}
	}
}

func TestPlaylistStructure(t *testing.T) {
	seq := []uint8{1, 0, 1}
	out, _ := build(t, watermarkedMeta(3), seq, Options{})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	if lines[0] != "#EXTM3U" {
		t.Errorf("first line = %q, want #EXTM3U", lines[0])
	}
	if last := lines[len(lines)-1]; last != "#EXT-X-ENDLIST" {
		t.Errorf("last line = %q, want #EXT-X-ENDLIST", last)
	}
	for _, tag := range []string{"#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:4", "#EXT-X-PLAYLIST-TYPE:VOD"} {
		if !strings.Contains(out, tag) {
			t.Errorf("playlist is missing %s", tag)
		}
	}
	if n := strings.Count(out, "#EXTINF:"); n != 3 {
		t.Errorf("playlist has %d EXTINF tags, want 3", n)
	}
}

// A URI line following EXTINF is what a player fetches; a stray blank line or
// misplaced tag makes hls.js drop the segment.
func TestEveryExtinfIsFollowedByAURI(t *testing.T) {
	seq := []uint8{0, 1, 1, 0}
	out, _ := build(t, watermarkedMeta(4), seq, Options{})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "#EXTINF:") {
			continue
		}
		if i+1 >= len(lines) {
			t.Fatal("playlist ends on an EXTINF tag")
		}
		next := lines[i+1]
		if next == "" || strings.HasPrefix(next, "#") {
			t.Errorf("EXTINF at line %d is followed by %q, want a URI", i, next)
		}
	}
}

func TestTargetDurationRoundsUp(t *testing.T) {
	meta := watermarkedMeta(2)
	meta.SegmentDuration = 4.5
	meta.TotalDuration = 9
	out, _ := build(t, meta, []uint8{0, 1}, Options{})
	if !strings.Contains(out, "#EXT-X-TARGETDURATION:5") {
		t.Error("target duration must round up to an integer at least the segment length")
	}
}

func TestFinalSegmentDurationIsExact(t *testing.T) {
	meta := watermarkedMeta(3)
	meta.TotalDuration = 10 // final segment runs 2s, not 4s
	out, _ := build(t, meta, []uint8{0, 0, 0}, Options{})
	if !strings.Contains(out, "#EXTINF:2.000,") {
		t.Errorf("final segment duration not declared exactly:\n%s", out)
	}
}

func TestSessionDataCarriesOverlayText(t *testing.T) {
	seq := []uint8{0, 1}
	const overlay = "viewer@example.com · order-42"
	out, _ := build(t, watermarkedMeta(2), seq, Options{OverlayText: overlay})
	want := OverlayTag + base64.StdEncoding.EncodeToString([]byte(overlay))
	if !strings.Contains(out, want) {
		t.Errorf("playlist is missing the overlay tag:\n%s", out)
	}
}

// Overlay text is opaque and unvalidated, so it must not be able to inject tags.
func TestSessionDataCannotBreakOutOfTheAttribute(t *testing.T) {
	seq := []uint8{0, 1}
	out, _ := build(t, watermarkedMeta(2), seq, Options{
		OverlayText: "evil\"\n#EXT-X-ENDLIST\n#EXTINF:1,\nhttps://attacker.example/x.ts",
	})
	// The payload may appear inside the quoted attribute value, where it is
	// inert; what matters is that it never becomes a line of its own.
	var endlists, injected int
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		switch {
		case line == "#EXT-X-ENDLIST":
			endlists++
		case strings.Contains(line, "attacker.example") && !strings.HasPrefix(line, "#EXT-X-SESSION-DATA:"):
			injected++
		}
	}
	if endlists != 1 {
		t.Errorf("playlist has %d ENDLIST lines, want 1:\n%s", endlists, out)
	}
	if injected != 0 {
		t.Errorf("overlay text produced %d injected lines:\n%s", injected, out)
	}
}

func TestSignerErrorIsWrapped(t *testing.T) {
	fs := &fakeSigner{err: fmt.Errorf("credentials expired")}
	b, _ := NewBuilder(fs)
	_, err := b.Build(context.Background(), watermarkedMeta(2), []uint8{0, 1}, Options{SegmentTTL: time.Hour})
	if err == nil {
		t.Fatal("Build = nil error, want the signer error")
	}
	if !strings.Contains(err.Error(), "credentials expired") {
		t.Errorf("error %q does not mention the signer failure", err)
	}
}

func TestGoldenPlaylist(t *testing.T) {
	seq := []uint8{1, 0, 1, 1, 0}
	meta := watermarkedMeta(5)
	meta.TotalDuration = 18 // final segment runs 2s
	out, _ := build(t, meta, seq, Options{
		SegmentTTL:  time.Hour,
		OverlayText: "viewer@example.com",
	})

	golden := filepath.Join("testdata", "playlist.m3u8")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden file (regenerate with UPDATE_GOLDEN=1): %v", err)
	}
	if out != string(want) {
		t.Errorf("playlist does not match the golden file\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}
