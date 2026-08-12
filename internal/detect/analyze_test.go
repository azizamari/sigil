package detect

import (
	"context"
	"math"
	"math/rand"
	"testing"

	"github.com/azizamari/sigil/internal/embed"
)

// fakeAnalyzer renders synthetic frames carrying a chosen variant, so the
// sequence-building logic is testable without an encoder.
type fakeAnalyzer struct {
	params   embed.Params
	pattern  *embed.Pattern
	variants []uint8
	noise    float64
	seed     int64
	// unmarked renders content with no watermark at all.
	unmarked bool
	calls    int
}

func (f *fakeAnalyzer) Frames(_ context.Context, src string, limit int) ([]*embed.Integral, error) {
	f.calls++
	idx := 0
	if _, err := fmtSscan(src, &idx); err != nil {
		return nil, err
	}
	variant := -1
	if !f.unmarked {
		variant = int(f.variants[idx])
	}

	out := make([]*embed.Integral, 0, limit)
	for i := range limit {
		luma := synthFrame(f.params, f.pattern, variant, f.noise, f.seed+int64(idx*100+i))
		in, err := embed.NewIntegral(luma, f.params.Width, f.params.Height)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, nil
}

func fmtSscan(src string, out *int) (int, error) {
	v := 0
	for _, c := range src {
		if c >= '0' && c <= '9' {
			v = v*10 + int(c-'0')
		}
	}
	*out = v
	return 1, nil
}

func synthFrame(p embed.Params, pat *embed.Pattern, variant int, noise float64, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	buf := make([]byte, p.Width*p.Height)
	for y := range p.Height {
		for x := range p.Width {
			base := 100 + 50*math.Sin(float64(x)/80) + 30*math.Cos(float64(y)/60)
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
			v := int(base + 0.5)
			if v < 0 {
				v = 0
			}
			if v > 255 {
				v = 255
			}
			buf[y*p.Width+x] = byte(v)
		}
	}
	return buf
}

func partNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "part_" + itoa(i) + ".ts"
	}
	return out
}

func TestSoftSequenceTracksTheEmbeddedVariants(t *testing.T) {
	p := embed.DefaultParams(640, 360)
	p.Amplitude = 8
	pat, err := embed.NewPattern(p)
	if err != nil {
		t.Fatal(err)
	}
	variants := []uint8{1, 0, 0, 1, 1, 0, 1, 0, 1, 1, 0, 0}
	a := &fakeAnalyzer{params: p, pattern: pat, variants: variants, noise: 2, seed: 1}

	soft, err := softSequenceFor(context.Background(), a, partNames(len(variants)), p)
	if err != nil {
		t.Fatalf("softSequenceFor: %v", err)
	}
	if len(soft) != len(variants) {
		t.Fatalf("got %d soft decisions, want %d", len(soft), len(variants))
	}
	for i, want := range variants {
		// Positive favours variant 1, matching the codebook convention.
		if (soft[i] > 0) != (want == 1) {
			t.Errorf("segment %d: soft %+.3f does not indicate variant %d", i, soft[i], want)
		}
	}
}

// Locating the grid once is what keeps the null from being re-inflated for
// every bit, so the analyzer must not be asked to search per segment.
func TestAlignmentIsSearchedOnceNotPerSegment(t *testing.T) {
	p := embed.DefaultParams(640, 360)
	p.Amplitude = 8
	pat, _ := embed.NewPattern(p)
	variants := make([]uint8, 24)
	for i := range variants {
		variants[i] = uint8(i % 2)
	}
	a := &fakeAnalyzer{params: p, pattern: pat, variants: variants, noise: 1, seed: 2}

	if _, err := softSequenceFor(context.Background(), a, partNames(len(variants)), p); err != nil {
		t.Fatal(err)
	}
	// Frames are cached per segment, so each is decoded exactly once however
	// many probes and decoy passes run over it.
	if a.calls != len(variants) {
		t.Errorf("decoded %d times for %d segments; frames should be cached", a.calls, len(variants))
	}
}

func TestSoftSequenceOnUnmarkedContentStaysNearZero(t *testing.T) {
	p := embed.DefaultParams(640, 360)
	p.Amplitude = 8
	pat, _ := embed.NewPattern(p)
	variants := make([]uint8, 12)

	marked := &fakeAnalyzer{params: p, pattern: pat, variants: variants, noise: 2, seed: 3}
	clean := &fakeAnalyzer{params: p, pattern: pat, variants: variants, noise: 2, seed: 3, unmarked: true}

	markedSoft, err := softSequenceFor(context.Background(), marked, partNames(len(variants)), p)
	if err != nil {
		t.Fatal(err)
	}
	cleanSoft, err := softSequenceFor(context.Background(), clean, partNames(len(variants)), p)
	if err != nil {
		t.Fatal(err)
	}

	if meanAbs(cleanSoft) >= meanAbs(markedSoft) {
		t.Errorf("unmarked content averaged %.3f against a real mark's %.3f",
			meanAbs(cleanSoft), meanAbs(markedSoft))
	}
}

func meanAbs(s SoftSequence) float64 {
	var total float64
	for _, v := range s {
		total += math.Abs(v)
	}
	return total / float64(len(s))
}
