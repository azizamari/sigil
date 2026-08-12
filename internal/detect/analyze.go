package detect

import (
	"context"
	"fmt"
	"math"

	"github.com/azizamari/sigil/internal/embed"
)

// Analyzer decodes frames from a media file. Detection works from frames rather
// than from a per-segment soft decision so the block grid can be located once
// for the whole leak instead of re-derived for every segment.
type Analyzer interface {
	Frames(ctx context.Context, src string, limit int) ([]*embed.Integral, error)
}

// framesPerSegment caps decoding. The mark is present in every frame, so a
// handful is enough and the rest is wasted decode time.
const framesPerSegment = 6

// alignmentProbes is how many segments are searched to locate the grid. The
// crop and shift belong to the file, not the segment, so a few confident
// samples settle it for all of them.
const alignmentProbes = 3

// softSequenceFor turns the segments of a leak into one soft decision each.
//
// The alignment search runs on a few probe segments and the winner is then
// applied to every segment. Searching per segment would repeat the same work
// and, worse, take a fresh maximum over thousands of candidates each time,
// lifting the null for every bit independently.
func softSequenceFor(ctx context.Context, a Analyzer, parts []string, p embed.Params) (SoftSequence, error) {
	pat, err := embed.NewPattern(p)
	if err != nil {
		return nil, err
	}
	opts := embed.DefaultSearch()

	framesFor := make([][]*embed.Integral, len(parts))
	load := func(i int) ([]*embed.Integral, error) {
		if framesFor[i] == nil {
			f, err := a.Frames(ctx, parts[i], framesPerSegment)
			if err != nil {
				return nil, err
			}
			framesFor[i] = f
		}
		return framesFor[i], nil
	}

	var alignment embed.Alignment
	var bestProbe float64
	probes := min(alignmentProbes, len(parts))
	for i := range probes {
		// Probe segments are spread across the leak so a single damaged stretch
		// does not decide the grid for everything.
		idx := i * len(parts) / probes
		frames, err := load(idx)
		if err != nil {
			return nil, err
		}
		if len(frames) == 0 {
			continue
		}
		candidate, score := embed.SearchAlignment(frames, pat, p, opts)
		if math.Abs(score) > math.Abs(bestProbe) {
			alignment, bestProbe = candidate, score
		}
	}
	if bestProbe == 0 {
		return nil, fmt.Errorf("detect: could not locate the watermark grid in %d probe segments", probes)
	}

	// The null is measured at the settled alignment, across the same segments,
	// so a soft decision means "this much stronger than a pattern the content
	// never carried" rather than "this many luma codes".
	null, err := nullAtAlignment(ctx, load, len(parts), pat, p, alignment)
	if err != nil {
		return nil, err
	}

	soft := make(SoftSequence, 0, len(parts))
	for i := range parts {
		frames, err := load(i)
		if err != nil {
			return nil, err
		}
		if len(frames) == 0 {
			soft = append(soft, 0)
			continue
		}
		score := embed.ScoreFrames(frames, pat, p, alignment)
		soft = append(soft, embed.SoftDecision(score, null))
	}
	return soft, nil
}

// nullAtAlignment scores decoy patterns the content was never marked with,
// giving the level a genuine bit has to beat for this leak.
func nullAtAlignment(ctx context.Context, load func(int) ([]*embed.Integral, error), count int, _ *embed.Pattern, p embed.Params, a embed.Alignment) (float64, error) {
	probes := min(alignmentProbes, count)
	var peak float64
	for _, seed := range embed.DecoySeeds(p.Seed) {
		decoyParams := p
		decoyParams.Seed = seed
		decoy, err := embed.NewPattern(decoyParams)
		if err != nil {
			return 0, err
		}
		for i := range probes {
			frames, err := load(i * count / probes)
			if err != nil {
				return 0, err
			}
			if len(frames) == 0 {
				continue
			}
			if s := math.Abs(embed.ScoreFrames(frames, decoy, decoyParams, a)); s > peak {
				peak = s
			}
		}
	}
	if peak < 1e-9 {
		peak = 1e-9
	}
	return peak, nil
}
