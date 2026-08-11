package pack

import (
	"errors"
	"testing"
	"time"

	"github.com/azizamari/sigil/internal/codebook"
)

func TestPlanChoosesAWorkableCodebook(t *testing.T) {
	meta, ep, err := Plan(1280, 720, 1200, Options{
		AssetID:         "lecture-01",
		SegmentDuration: 0.75,
		DetectWindow:    90 * time.Second,
		Sessions:        10000,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if meta.SegmentCount != 1600 {
		t.Errorf("segment count = %d, want 1600", meta.SegmentCount)
	}
	if !meta.Watermarked || meta.Codebook == nil || meta.Embed == nil {
		t.Fatal("plan must produce a watermarked asset with both parameter sets")
	}

	book, err := codebook.New(*meta.Codebook)
	if err != nil {
		t.Fatalf("codebook from plan: %v", err)
	}
	if book.Capacity() < 10000 {
		t.Errorf("capacity %d, want at least 10000 sessions", book.Capacity())
	}
	if windowSegments := int(90 / 0.75); book.ConfidentWindow() > windowSegments {
		t.Errorf("confident window %d segments exceeds the %d available in a 90s clip",
			book.ConfidentWindow(), windowSegments)
	}
	if ep.Width != 1280 || ep.Height != 720 {
		t.Errorf("embed params sized %dx%d, want 1280x720", ep.Width, ep.Height)
	}
}

// A configuration that cannot deliver attribution must fail before an hour of
// encoding, not produce an undetectable asset.
func TestPlanRefusesImpossibleConfigurations(t *testing.T) {
	tests := []struct {
		name string
		o    Options
	}{
		{"4s segments cannot carry 10k sessions in 90s", Options{
			AssetID: "a", SegmentDuration: 4, DetectWindow: 90 * time.Second, Sessions: 10000,
		}},
		{"2s segments cannot either", Options{
			AssetID: "a", SegmentDuration: 2, DetectWindow: 90 * time.Second, Sessions: 10000,
		}},
		{"1s segments leave 90, three short of the 93 needed", Options{
			AssetID: "a", SegmentDuration: 1, DetectWindow: 90 * time.Second, Sessions: 10000,
		}},
		{"a 10 second window is far too short", Options{
			AssetID: "a", SegmentDuration: 0.75, DetectWindow: 10 * time.Second, Sessions: 10000,
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Plan(1280, 720, 1200, tc.o)
			if !errors.Is(err, codebook.ErrNoFit) {
				t.Fatalf("Plan = %v, want ErrNoFit", err)
			}
		})
	}
}

func TestPlanValidatesInputs(t *testing.T) {
	tests := []struct {
		name     string
		w, h     int
		duration float64
		o        Options
	}{
		{name: "bad asset id", w: 1280, h: 720, duration: 600, o: Options{AssetID: "../etc", SegmentDuration: 1}},
		{name: "zero duration", w: 1280, h: 720, duration: 0, o: Options{AssetID: "a", SegmentDuration: 1}},
		{name: "frame too small for a usable grid", w: 160, h: 120, duration: 600, o: Options{AssetID: "a", SegmentDuration: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Plan(tc.w, tc.h, tc.duration, tc.o); err == nil {
				t.Fatal("Plan = nil error, want error")
			}
		})
	}
}

func TestPlanDefaultsToADetectableConfiguration(t *testing.T) {
	meta, ep, err := Plan(1920, 1080, 600, Options{AssetID: "lecture-01"})
	if err != nil {
		t.Fatalf("Plan with defaults: %v", err)
	}
	if meta.SegmentDuration != 0.75 {
		t.Errorf("default segment duration = %v, want 0.75s", meta.SegmentDuration)
	}
	// The default amplitude has to clear the null the alignment search lifts.
	if ep.Amplitude < 4 {
		t.Errorf("default amplitude %d is below what crop search needs", ep.Amplitude)
	}
	nx, ny := ep.Blocks()
	if nx*ny < 400 {
		t.Errorf("default grid %dx%d is too coarse to detect", nx, ny)
	}
}

func TestBitsFor(t *testing.T) {
	tests := []struct {
		sessions uint64
		want     int
	}{
		{sessions: 2, want: 1},
		{sessions: 3, want: 2},
		{sessions: 256, want: 8},
		{sessions: 10000, want: 14},
		{sessions: 65536, want: 16},
	}
	for _, tc := range tests {
		if got := bitsFor(tc.sessions); got != tc.want {
			t.Errorf("bitsFor(%d) = %d, want %d", tc.sessions, got, tc.want)
		}
	}
}

func TestPlanSeedFlowsIntoBothParameterSets(t *testing.T) {
	meta, ep, err := Plan(1280, 720, 600, Options{AssetID: "a", Seed: 4242})
	if err != nil {
		t.Fatal(err)
	}
	if meta.Codebook.Seed != 4242 || ep.Seed != 4242 {
		t.Errorf("seed did not reach both parameter sets: codebook %d, embed %d",
			meta.Codebook.Seed, ep.Seed)
	}
}
