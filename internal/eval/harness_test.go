package eval

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttackGridCoversTheSpecMatrix(t *testing.T) {
	grid := AttackGrid()
	byName := map[string]Attack{}
	for _, a := range grid {
		byName[a.Name] = a
	}

	// SPEC 10 names these explicitly; a silently missing row would make the
	// published table look better than the system is.
	for _, want := range []string{
		"clean", "reencode_40", "reencode_60", "reencode_80",
		"scale_720", "scale_480", "crop_2", "crop_5", "crop_10",
		"framerate_24", "combined", "collusion_average",
	} {
		if _, ok := byName[want]; !ok {
			t.Errorf("attack grid is missing %q", want)
		}
	}
	if !byName["collusion_average"].Collusion {
		t.Error("the collusion row must be flagged so it is never gated on")
	}
}

func TestFixturesCoverThreeContentTypes(t *testing.T) {
	fixtures := Fixtures(1280, 720, 30, 10)
	if len(fixtures) != 3 {
		t.Fatalf("got %d fixtures, want 3", len(fixtures))
	}
	names := map[string]bool{}
	for _, f := range fixtures {
		names[f.Name] = true
		if f.Lavfi == "" {
			t.Errorf("fixture %q has no filter graph", f.Name)
		}
	}
	// The screencast is the hard case and the dominant e-learning content type.
	if !names["screencast"] {
		t.Error("fixtures must include a screencast")
	}
	if !names["high_motion"] || !names["talking_head"] {
		t.Error("fixtures must cover motion and talking-head content")
	}
}

// The screencast graph must actually move; a static deck compresses to a few
// kbps and turns the bitrate attacks into nonsense.
func TestScreencastFixtureIsNotStatic(t *testing.T) {
	graph := screencastGraph(1280, 720, 30, 12)
	for _, want := range []string{"crop=", "drawbox", "sin(t", "cos(t"} {
		if !strings.Contains(graph, want) {
			t.Errorf("screencast graph is missing %q, so it has no motion", want)
		}
	}
}

func TestCompareToBaselineFlagsRealMovementOnly(t *testing.T) {
	baseline := Report{Threshold: 0.9, Rates: []Rate{
		{Fixture: "high_motion", Attack: "reencode_60", ClipLen: 90, TPR: 0.98, FPR: 0.001},
		{Fixture: "screencast", Attack: "reencode_60", ClipLen: 90, TPR: 0.80, FPR: 0.002},
		{Fixture: "high_motion", Attack: "collusion_average", ClipLen: 90, TPR: 0.10, Collusion: true},
	}}

	tests := []struct {
		name    string
		current Report
		want    int
	}{
		{
			name: "noise within the margin is not a regression",
			current: Report{Rates: []Rate{
				{Fixture: "high_motion", Attack: "reencode_60", ClipLen: 90, TPR: 0.96, FPR: 0.001},
			}},
			want: 0,
		},
		{
			name: "a real accuracy drop is flagged",
			current: Report{Rates: []Rate{
				{Fixture: "high_motion", Attack: "reencode_60", ClipLen: 90, TPR: 0.70, FPR: 0.001},
			}},
			want: 1,
		},
		{
			name: "a rise in false positives is flagged",
			current: Report{Rates: []Rate{
				{Fixture: "screencast", Attack: "reencode_60", ClipLen: 90, TPR: 0.80, FPR: 0.20},
			}},
			want: 1,
		},
		{
			name: "collusion is never gated on",
			current: Report{Rates: []Rate{
				{Fixture: "high_motion", Attack: "collusion_average", ClipLen: 90, TPR: 0.0, Collusion: true},
			}},
			want: 0,
		},
		{
			name: "an unknown cell is ignored rather than failing the build",
			current: Report{Rates: []Rate{
				{Fixture: "brand_new", Attack: "reencode_60", ClipLen: 90, TPR: 0.0},
			}},
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CompareToBaseline(tc.current, baseline, 0.05)
			if len(got) != tc.want {
				t.Errorf("got %d regressions, want %d: %v", len(got), tc.want, got)
			}
		})
	}
}

func TestReportRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	report := Report{Threshold: 0.9, Rates: []Rate{
		{Fixture: "screencast", Attack: "crop_5", ClipLen: 90, Trials: 20, TPR: 0.85, FPR: 0.0},
		{Fixture: "high_motion", Attack: "clean", ClipLen: 30, Trials: 20, TPR: 1.0, FPR: 0.0},
	}}
	if err := report.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadReport(path)
	if err != nil {
		t.Fatalf("LoadReport: %v", err)
	}
	if len(got.Rates) != 2 || got.Threshold != 0.9 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	// Sorting keeps the committed baseline diffable instead of reordering
	// randomly on every run.
	if got.Rates[0].Fixture != "high_motion" {
		t.Errorf("report was not sorted: first row is %q", got.Rates[0].Fixture)
	}
	if _, err := LoadReport(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("LoadReport of a missing file = nil error, want error")
	}
}

func TestMarkdownMarksTheExpectedFailure(t *testing.T) {
	report := Report{Threshold: 0.9, Rates: []Rate{
		{Fixture: "high_motion", Attack: "collusion_average", ClipLen: 90, TPR: 0.1, Collusion: true},
	}}
	md := report.Markdown()
	if !strings.Contains(md, "expected failure") {
		t.Errorf("collusion row is not marked as an expected failure:\n%s", md)
	}
	if !strings.Contains(md, "TPR") || !strings.Contains(md, "FPR") {
		t.Error("the table must report both directions")
	}
}

func TestApplyProducesADegradedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("needs ffmpeg; this is an L2 test")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	out, err := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=15:duration=2",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "20", "-pix_fmt", "yuv420p", src).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}

	for _, a := range []Attack{
		{Name: "crop_5", Filters: "crop=iw*0.90:ih*0.90", BitrateFraction: 0.6},
		{Name: "framerate_24", FrameRate: 24, BitrateFraction: 0.6},
	} {
		got, err := Apply(context.Background(), a, src, dir, 2_000_000)
		if err != nil {
			t.Fatalf("Apply(%s): %v", a.Name, err)
		}
		if got == "" {
			t.Fatalf("Apply(%s) returned no path", a.Name)
		}
	}
}
