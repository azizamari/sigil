package embed

import (
	"math"
	"math/rand"
	"testing"
)

func testParams() Params {
	p := DefaultParams(1280, 720)
	p.Amplitude = 4
	return p
}

// synth paints a frame with smooth content and, optionally, the watermark for
// a variant. It is how the correlator is tested without touching video.
func synth(p Params, pat *Pattern, variant int, noise float64, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	buf := make([]byte, p.Width*p.Height)
	for y := range p.Height {
		for x := range p.Width {
			// A smooth gradient plus a broad blob: content the neighbour
			// subtraction is supposed to remove.
			base := 90 + 60*math.Sin(float64(x)/90) + 40*math.Cos(float64(y)/70)
			if variant >= 0 {
				polarity := 1.0
				if variant == 1 {
					polarity = -1
				}
				base += polarity * float64(pat.At(x/p.Block, y/p.Block)) * float64(p.Amplitude)
			}
			if noise > 0 {
				base += r.NormFloat64() * noise
			}
			buf[y*p.Width+x] = byte(clampInt(int(base+0.5), 0, 255))
		}
	}
	return buf
}

func integralOf(t *testing.T, luma []byte, p Params) *Integral {
	t.Helper()
	in, err := NewIntegral(luma, p.Width, p.Height)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestParamsValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Params)
		wantErr bool
	}{
		{name: "defaults", mutate: func(*Params) {}},
		{name: "bad version", mutate: func(p *Params) { p.Version = 99 }, wantErr: true},
		{name: "zero size", mutate: func(p *Params) { p.Width = 0 }, wantErr: true},
		{name: "partial trailing blocks are allowed", mutate: func(p *Params) { p.Width = 1290 }},
		{name: "grid too coarse to detect", mutate: func(p *Params) { p.Block = 200 }, wantErr: true},
		{name: "zero amplitude", mutate: func(p *Params) { p.Amplitude = 0 }, wantErr: true},
		{name: "absurd amplitude", mutate: func(p *Params) { p.Amplitude = 200 }, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams()
			tc.mutate(&p)
			if err := p.Validate(); tc.wantErr != (err != nil) {
				t.Errorf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// The pattern is persisted only as a seed, so the same seed must produce the
// same grid on any build, forever.
func TestPatternIsDeterministicAcrossRuns(t *testing.T) {
	p := testParams()
	a, err := NewPattern(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewPattern(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a.Sign {
		if a.Sign[i] != b.Sign[i] {
			t.Fatalf("block %d differs between runs", i)
		}
	}

	p.Seed = 2
	c, err := NewPattern(p)
	if err != nil {
		t.Fatal(err)
	}
	same := 0
	for i := range a.Sign {
		if a.Sign[i] == c.Sign[i] {
			same++
		}
	}
	if ratio := float64(same) / float64(len(a.Sign)); ratio > 0.7 {
		t.Errorf("different seeds agree on %.0f%% of blocks, want near 50%%", ratio*100)
	}
}

func TestPatternIsBalanced(t *testing.T) {
	pat, err := NewPattern(DefaultParams(1920, 1080))
	if err != nil {
		t.Fatal(err)
	}
	var sum int
	for _, s := range pat.Sign {
		sum += int(s)
	}
	if bias := math.Abs(float64(sum)) / float64(len(pat.Sign)); bias > 0.15 {
		t.Errorf("pattern sign bias %.2f, want a near-balanced grid", bias)
	}
}

func TestGrayPlaneCarriesTheAmplitude(t *testing.T) {
	p := testParams()
	pat, _ := NewPattern(p)
	for _, variant := range []uint8{0, 1} {
		plane, err := pat.GrayPlane(p, variant)
		if err != nil {
			t.Fatal(err)
		}
		polarity := 1
		if variant == 1 {
			polarity = -1
		}
		for by := range pat.NY {
			for bx := range pat.NX {
				got := int(plane[(by*p.Block)*p.Width+bx*p.Block])
				want := 128 + int(pat.At(bx, by))*polarity*p.Amplitude
				if got != want {
					t.Fatalf("variant %d block (%d,%d) = %d, want %d", variant, bx, by, got, want)
				}
			}
		}
	}
	if _, err := pat.GrayPlane(p, 2); err == nil {
		t.Error("GrayPlane with variant 2 = nil error, want error")
	}
}

// Variant 0 and variant 1 differ only in sign, which is what lets one
// correlation decide between them.
func TestScoreSeparatesVariants(t *testing.T) {
	p := testParams()
	pat, _ := NewPattern(p)
	aligned := Alignment{}

	v0 := Score(integralOf(t, synth(p, pat, 0, 0, 1), p), pat, p, aligned)
	v1 := Score(integralOf(t, synth(p, pat, 1, 0, 1), p), pat, p, aligned)
	null := Score(integralOf(t, synth(p, pat, -1, 0, 1), p), pat, p, aligned)

	if v0 <= 0 || v1 >= 0 {
		t.Fatalf("variants did not separate: v0 %+.2f, v1 %+.2f", v0, v1)
	}
	if math.Abs(null) > math.Abs(v0)/3 {
		t.Errorf("unwatermarked content scored %+.2f against a signal of %+.2f", null, v0)
	}
	if got := math.Abs(v0); got < float64(p.Amplitude)*0.6 {
		t.Errorf("recovered amplitude %.2f, want near %d", got, p.Amplitude)
	}
}

func TestSoftDecisionMatchesCodebookConvention(t *testing.T) {
	p := testParams()
	pat, _ := NewPattern(p)

	// Positive soft decisions favour variant 1 in the codebook.
	const null = 1.0
	v0 := SoftDecision(Score(integralOf(t, synth(p, pat, 0, 0, 3), p), pat, p, Alignment{}), null)
	v1 := SoftDecision(Score(integralOf(t, synth(p, pat, 1, 0, 3), p), pat, p, Alignment{}), null)
	if v0 >= 0 {
		t.Errorf("variant 0 soft decision = %+.2f, want negative", v0)
	}
	if v1 <= 0 {
		t.Errorf("variant 1 soft decision = %+.2f, want positive", v1)
	}
	for _, v := range []float64{v0, v1} {
		if v < -1 || v > 1 {
			t.Errorf("soft decision %+.2f escapes [-1, 1]", v)
		}
	}
}

func TestScoreSurvivesNoise(t *testing.T) {
	p := testParams()
	pat, _ := NewPattern(p)
	for _, noise := range []float64{2, 5, 10} {
		var correct int
		const trials = 20
		for i := range trials {
			variant := i % 2
			frame := synth(p, pat, variant, noise, int64(i)+100)
			soft := SoftDecision(Score(integralOf(t, frame, p), pat, p, Alignment{}), 1.0)
			if (soft > 0) == (variant == 1) {
				correct++
			}
		}
		if correct < trials {
			t.Errorf("noise sd %.0f: %d/%d frames decided correctly", noise, correct, trials)
		}
	}
}

func TestIntegralMeanMatchesDirectAverage(t *testing.T) {
	p := testParams()
	pat, _ := NewPattern(p)
	luma := synth(p, pat, 0, 3, 7)
	in := integralOf(t, luma, p)

	for _, r := range [][4]int{{0, 0, 40, 40}, {80, 40, 200, 160}, {1200, 700, 1280, 720}} {
		var sum float64
		for y := r[1]; y < r[3]; y++ {
			for x := r[0]; x < r[2]; x++ {
				sum += float64(luma[y*p.Width+x])
			}
		}
		want := sum / float64((r[2]-r[0])*(r[3]-r[1]))
		if got := in.Mean(r[0], r[1], r[2], r[3]); math.Abs(got-want) > 1e-9 {
			t.Errorf("Mean%v = %.6f, want %.6f", r, got, want)
		}
	}
}

func TestIntegralClampsOutOfBoundsRects(t *testing.T) {
	p := testParams()
	pat, _ := NewPattern(p)
	in := integralOf(t, synth(p, pat, 0, 0, 9), p)

	if got := in.Mean(-50, -50, 10, 10); got <= 0 {
		t.Errorf("clamped rect returned %.2f, want a real mean", got)
	}
	if got := in.Mean(2000, 2000, 2100, 2100); got != 0 {
		t.Errorf("fully out-of-bounds rect returned %.2f, want 0", got)
	}
	if got := in.Mean(100, 100, 50, 50); got != 0 {
		t.Errorf("inverted rect returned %.2f, want 0", got)
	}
}

func TestNewIntegralRejectsBadInput(t *testing.T) {
	if _, err := NewIntegral(make([]byte, 10), 0, 10); err == nil {
		t.Error("zero width = nil error, want error")
	}
	if _, err := NewIntegral(make([]byte, 10), 100, 100); err == nil {
		t.Error("short plane = nil error, want error")
	}
}

// Spike 2: a shifted grid is recoverable, but only because the detector looks
// for it. This is the property the whole crop attack rests on.
func TestSearchRecoversAShiftedGrid(t *testing.T) {
	p := testParams()
	pat, _ := NewPattern(p)
	frame := synth(p, pat, 0, 1, 11)

	shifted := shiftFrame(frame, p, 6, 4)
	in := integralOf(t, shifted, p)

	atZero := Score(in, pat, p, Alignment{})
	found, score := SearchAlignment([]*Integral{in}, pat, p, SearchOptions{
		MaxCrop: 0, MaxShift: 12, CropStep: 1, ShiftStep: 2,
	})
	if math.Abs(score) <= math.Abs(atZero) {
		t.Fatalf("search found %+.2f at %+v, no better than %+.2f at zero offset", score, found, atZero)
	}
	if score <= 0 {
		t.Errorf("recovered score %+.2f has the wrong sign for variant 0", score)
	}
}

func TestSearchOnUnwatermarkedContentStaysBelowASignal(t *testing.T) {
	p := testParams()
	pat, _ := NewPattern(p)

	signal := integralOf(t, synth(p, pat, 0, 2, 21), p)
	null := integralOf(t, synth(p, pat, -1, 2, 22), p)
	opts := SearchOptions{MaxCrop: 0.08, MaxShift: 8, CropStep: 0.02, ShiftStep: 4}

	_, signalScore := SearchAlignment([]*Integral{signal}, pat, p, opts)
	_, nullScore := SearchAlignment([]*Integral{null}, pat, p, opts)

	// Searching lifts the null off zero; the margin is what a confidence
	// threshold has to be built from, and it must not vanish.
	if math.Abs(nullScore) >= math.Abs(signalScore) {
		t.Errorf("null peaked at %+.2f against a signal of %+.2f", nullScore, signalScore)
	}
}

func TestScoreFramesAveragesAndHandlesEmpty(t *testing.T) {
	p := testParams()
	pat, _ := NewPattern(p)
	frames := []*Integral{
		integralOf(t, synth(p, pat, 0, 1, 31), p),
		integralOf(t, synth(p, pat, 0, 1, 32), p),
	}
	if got := ScoreFrames(frames, pat, p, Alignment{}); got <= 0 {
		t.Errorf("averaged score = %+.2f, want positive for variant 0", got)
	}
	if got := ScoreFrames(nil, pat, p, Alignment{}); got != 0 {
		t.Errorf("no frames = %+.2f, want 0", got)
	}
}

func shiftFrame(src []byte, p Params, dx, dy int) []byte {
	out := make([]byte, len(src))
	for y := range p.Height {
		for x := range p.Width {
			sx, sy := x+dx, y+dy
			if sx < 0 || sx >= p.Width || sy < 0 || sy >= p.Height {
				continue
			}
			out[y*p.Width+x] = src[sy*p.Width+sx]
		}
	}
	return out
}
