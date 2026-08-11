package embed

import (
	"errors"
	"fmt"
	"math"
)

// Alignment describes where the embedded grid sits in a frame that may have
// been cropped, scaled or shifted since packaging.
type Alignment struct {
	Crop   float64
	DX, DY int
}

// SearchOptions bounds the alignment search. Every candidate is another draw
// from the null, so widening these raises the score unwatermarked content
// reaches and the confidence threshold has to move with it.
type SearchOptions struct {
	MaxCrop   float64
	MaxShift  int
	CropStep  float64
	ShiftStep int
}

func DefaultSearch() SearchOptions {
	return SearchOptions{MaxCrop: 0.12, MaxShift: 12, CropStep: 0.01, ShiftStep: 4}
}

// Integral is a summed-area table so a block mean costs four lookups whatever
// the search space size.
type Integral struct {
	W, H int
	sum  []float64
}

func NewIntegral(luma []byte, w, h int) (*Integral, error) {
	if w <= 0 || h <= 0 {
		return nil, errors.New("embed: frame dimensions must be positive")
	}
	if len(luma) < w*h {
		return nil, fmt.Errorf("embed: luma plane is %d bytes, want %d", len(luma), w*h)
	}
	in := &Integral{W: w, H: h, sum: make([]float64, (w+1)*(h+1))}
	for y := range h {
		var row float64
		for x := range w {
			row += float64(luma[y*w+x])
			in.sum[(y+1)*(w+1)+x+1] = in.sum[y*(w+1)+x+1] + row
		}
	}
	return in, nil
}

func (in *Integral) Mean(x0, y0, x1, y1 int) float64 {
	x0, y0 = clampInt(x0, 0, in.W), clampInt(y0, 0, in.H)
	x1, y1 = clampInt(x1, 0, in.W), clampInt(y1, 0, in.H)
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	s := in.sum[y1*(in.W+1)+x1] - in.sum[y0*(in.W+1)+x1] -
		in.sum[y1*(in.W+1)+x0] + in.sum[y0*(in.W+1)+x0]
	return s / float64((x1-x0)*(y1-y0))
}

// Score correlates one frame against the pattern under a candidate alignment.
// The result is in luma code units, so a clean variant-0 frame scores near
// +amplitude and variant 1 near -amplitude.
func Score(in *Integral, pat *Pattern, p Params, a Alignment) float64 {
	srcW := float64(p.Width) * (1 - 2*a.Crop)
	srcH := float64(p.Height) * (1 - 2*a.Crop)
	if srcW <= 0 || srcH <= 0 {
		return 0
	}
	sx := float64(in.W) / srcW
	sy := float64(in.H) / srcH
	ox := -float64(p.Width)*a.Crop*sx + float64(a.DX)
	oy := -float64(p.Height)*a.Crop*sy + float64(a.DY)

	means := make([]float64, pat.NX*pat.NY)
	valid := make([]bool, pat.NX*pat.NY)
	for by := range pat.NY {
		for bx := range pat.NX {
			x0 := int(float64(bx*p.Block)*sx + ox)
			y0 := int(float64(by*p.Block)*sy + oy)
			x1 := int(float64((bx+1)*p.Block)*sx + ox)
			y1 := int(float64((by+1)*p.Block)*sy + oy)
			// A block that falls outside the frame carries no evidence. Scoring
			// it as zero luma would invent a hard edge, and the search would
			// happily lock onto that instead of the watermark.
			if x0 < 0 || y0 < 0 || x1 > in.W || y1 > in.H || x1 <= x0 || y1 <= y0 {
				continue
			}
			means[by*pat.NX+bx] = in.Mean(x0, y0, x1, y1)
			valid[by*pat.NX+bx] = true
		}
	}
	return correlate(means, valid, pat)
}

// Subtracting the 8-neighbour mean removes scene content, which is smooth at
// block scale, and leaves the mark: neighbouring signs are independent so they
// average to roughly zero.
func correlate(means []float64, valid []bool, pat *Pattern) float64 {
	var sum float64
	var n, interior int
	for by := 1; by < pat.NY-1; by++ {
		for bx := 1; bx < pat.NX-1; bx++ {
			interior++
			var around float64
			ok := valid[by*pat.NX+bx]
			for dy := -1; dy <= 1 && ok; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if dx == 0 && dy == 0 {
						continue
					}
					if !valid[(by+dy)*pat.NX+bx+dx] {
						ok = false
						break
					}
					around += means[(by+dy)*pat.NX+bx+dx]
				}
			}
			if !ok {
				continue
			}
			sum += (means[by*pat.NX+bx] - around/8) * float64(pat.At(bx, by))
			n++
		}
	}
	// An alignment that only keeps a sliver of the grid is not evidence of
	// anything; rejecting it stops the search from preferring degenerate crops.
	if n == 0 || n*2 < interior {
		return 0
	}
	return sum / float64(n)
}

func ScoreFrames(frames []*Integral, pat *Pattern, p Params, a Alignment) float64 {
	if len(frames) == 0 {
		return 0
	}
	var total float64
	for _, in := range frames {
		total += Score(in, pat, p, a)
	}
	return total / float64(len(frames))
}

// SearchAlignment maximises the absolute score: the sign carries the variant,
// so the grid is located where the magnitude peaks in either direction.
func SearchAlignment(frames []*Integral, pat *Pattern, p Params, opts SearchOptions) (Alignment, float64) {
	if opts.CropStep <= 0 {
		opts.CropStep = 0.01
	}
	if opts.ShiftStep <= 0 {
		opts.ShiftStep = 4
	}

	var best Alignment
	var bestScore float64
	for crop := 0.0; crop <= opts.MaxCrop+1e-9; crop += opts.CropStep {
		for dy := -opts.MaxShift; dy <= opts.MaxShift; dy += opts.ShiftStep {
			for dx := -opts.MaxShift; dx <= opts.MaxShift; dx += opts.ShiftStep {
				a := Alignment{Crop: crop, DX: dx, DY: dy}
				if s := ScoreFrames(frames, pat, p, a); math.Abs(s) > math.Abs(bestScore) {
					best, bestScore = a, s
				}
			}
		}
	}
	return refine(frames, pat, p, best, bestScore, opts)
}

func refine(frames []*Integral, pat *Pattern, p Params, around Alignment, score float64, opts SearchOptions) (Alignment, float64) {
	best, bestScore := around, score
	for crop := around.Crop - opts.CropStep; crop <= around.Crop+opts.CropStep+1e-9; crop += opts.CropStep / 4 {
		for dy := around.DY - opts.ShiftStep; dy <= around.DY+opts.ShiftStep; dy++ {
			for dx := around.DX - opts.ShiftStep; dx <= around.DX+opts.ShiftStep; dx++ {
				a := Alignment{Crop: math.Max(0, crop), DX: dx, DY: dy}
				if s := ScoreFrames(frames, pat, p, a); math.Abs(s) > math.Abs(bestScore) {
					best, bestScore = a, s
				}
			}
		}
	}
	return best, bestScore
}

// SoftDecision maps a raw correlation to [-1, 1], where positive favours
// variant 1.
//
// It is normalised against a null measured on the same input under the same
// search, not against the embedded amplitude. Searching for an alignment takes
// a maximum over thousands of candidates, so unwatermarked content peaks well
// above zero; dividing by amplitude would report full confidence for content
// carrying no mark at all.
func SoftDecision(score, null float64) float64 {
	null = math.Abs(null)
	if null < 1e-9 {
		null = 1e-9
	}
	margin := (math.Abs(score) - null) / null
	margin = math.Max(0, math.Min(1, margin))
	// Score is positive for variant 0, which adds +sign, so the sign is flipped
	// to match the codebook convention that positive favours variant 1.
	if score > 0 {
		return -margin
	}
	return margin
}

// DecoySeeds derive patterns the content was never marked with. Their peak
// under the same search estimates how high this particular content and search
// space can score by chance.
func DecoySeeds(seed int64) []int64 {
	return []int64{seed ^ 0x5D65_C0DE, seed ^ 0x1234_ABCD}
}

// NullEstimate scores decoy patterns to find the level a genuine mark must beat.
func NullEstimate(frames []*Integral, p Params, opts SearchOptions) (float64, error) {
	var peak float64
	for _, seed := range DecoySeeds(p.Seed) {
		decoyParams := p
		decoyParams.Seed = seed
		decoy, err := NewPattern(decoyParams)
		if err != nil {
			return 0, err
		}
		if _, score := SearchAlignment(frames, decoy, decoyParams, opts); math.Abs(score) > peak {
			peak = math.Abs(score)
		}
	}
	return peak, nil
}
