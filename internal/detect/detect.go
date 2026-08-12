// Package detect attributes a leaked file to the session it was issued to.
//
// The output is evidence, not proof. Every result carries a confidence derived
// from a measured null distribution, and anyone using it to accuse a person
// needs to understand the error rate behind that number.
package detect

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/azizamari/sigil/internal/codebook"
	"github.com/azizamari/sigil/internal/embed"
	"github.com/azizamari/sigil/internal/storage"
)

var ErrNoMatch = errors.New("detect: no session matched above the confidence threshold")

// DefaultThreshold is deliberately strict. It is the score an unwatermarked or
// unissued input must not reach, and it is cheaper to fail to attribute a real
// leak than to name the wrong viewer.
const DefaultThreshold = 0.90

type Result struct {
	SessionID     string  `json:"session_id,omitempty"`
	PayloadID     uint64  `json:"payload_id"`
	Confidence    float64 `json:"confidence"`
	BitsRecovered int     `json:"bits_recovered"`
	BitsTotal     int     `json:"bits_total"`
	Offset        int     `json:"offset"`
	Matched       bool    `json:"matched"`
	// NullPeak is the best score reached by sequences that were never issued.
	// Reporting it alongside the match is what makes the confidence auditable.
	NullPeak float64 `json:"null_peak"`
}

// Extractor reads one soft decision per segment file.
type Extractor interface {
	Extract(ctx context.Context, src string, p embed.Params) (float64, error)
}

// Splitter cuts a leaked file into per-segment pieces so each can be scored.
// startOffset is part of the contract because a leak carries no segment
// boundaries of its own and the detector has to try several.
type Splitter interface {
	Split(ctx context.Context, src string, segmentSeconds, startOffset float64, outDir string) ([]string, error)
}

type Detector struct {
	// Analyzer decodes frames. Preferred over Extractor because it lets the
	// grid be located once for the whole leak.
	Analyzer  Analyzer
	Extractor Extractor
	Splitter  Splitter
	Threshold float64
}

// Issued is the set of sequences actually handed out. Matching against it, and
// against sequences that were not, is what separates evidence from a matcher
// that always names its closest candidate.
type Issued struct {
	SessionID string `json:"session_id"`
	PayloadID uint64 `json:"payload_id"`
}

// SoftSequence holds one confidence per recovered segment, positive favouring
// variant 1.
type SoftSequence []float64

// Attribute decodes a soft sequence and scores it against the issued set.
//
// A bare best match means nothing: with enough issued sequences there is always
// a closest one. The score is compared against the best score reachable by
// sequences that were never issued, so a leak from an unmarked source has
// somewhere to land other than an innocent viewer.
func Attribute(book *codebook.Codebook, soft SoftSequence, issued []Issued, threshold float64) (Result, error) {
	if book == nil {
		return Result{}, errors.New("detect: nil codebook")
	}
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	if len(soft) < book.MinWindow() {
		return Result{}, fmt.Errorf("detect: recovered %d segments, need at least %d",
			len(soft), book.MinWindow())
	}

	match, err := book.Decode(soft)
	if err != nil {
		return Result{}, fmt.Errorf("detect: decode: %w", err)
	}

	null, err := nullPeak(book, soft, issued, match.ID)
	if err != nil {
		return Result{}, err
	}

	res := Result{
		PayloadID:     match.ID,
		Confidence:    match.Score,
		BitsRecovered: len(soft) - match.Errors,
		BitsTotal:     len(soft),
		Offset:        match.Offset,
		NullPeak:      null,
	}
	for _, iss := range issued {
		if iss.PayloadID == match.ID {
			res.SessionID = iss.SessionID
			break
		}
	}

	// Both conditions matter. Clearing the absolute threshold shows the mark is
	// present; clearing the null shows it belongs to this session and not to
	// whichever candidate happened to score best.
	res.Matched = match.Score >= threshold && match.Score > null && res.SessionID != ""
	return res, nil
}

// nullPeak scores unissued sequences to find how well a wrong answer can fit
// this input. It is the empirical false-positive floor for this leak.
func nullPeak(book *codebook.Codebook, soft SoftSequence, issued []Issued, winner uint64) (float64, error) {
	taken := make(map[uint64]bool, len(issued))
	for _, iss := range issued {
		taken[iss.PayloadID] = true
	}

	var peak float64
	capacity := book.Capacity()
	const samples = 64
	for i := range uint64(samples) {
		// Spread the probes across the space rather than clustering at zero.
		candidate := (i*2654435761 + 12345) % capacity
		if taken[candidate] || candidate == winner {
			continue
		}
		seq, err := book.Sequence(candidate)
		if err != nil {
			return 0, fmt.Errorf("detect: null candidate %d: %w", candidate, err)
		}
		if s := correlateSequence(soft, seq); s > peak {
			peak = s
		}
	}
	return peak, nil
}

// correlateSequence scores a soft window against a known sequence at its best
// offset, normalised to [-1, 1].
func correlateSequence(soft SoftSequence, seq []uint8) float64 {
	if len(soft) == 0 || len(seq) < len(soft) {
		return 0
	}
	var best float64
	for start := 0; start+len(soft) <= len(seq); start++ {
		var num, den float64
		for j, v := range soft {
			sign := -1.0
			if seq[start+j] == 1 {
				sign = 1
			}
			num += v * sign
			den += math.Abs(v)
		}
		if den == 0 {
			continue
		}
		if s := num / den; s > best {
			best = s
		}
	}
	return best
}

// Ranked lists the issued sessions by how well each fits, so an investigator
// can see whether the winner stood out or merely edged out the field.
func Ranked(book *codebook.Codebook, soft SoftSequence, issued []Issued, limit int) ([]Result, error) {
	if book == nil {
		return nil, errors.New("detect: nil codebook")
	}
	out := make([]Result, 0, len(issued))
	for _, iss := range issued {
		seq, err := book.Sequence(iss.PayloadID)
		if err != nil {
			return nil, err
		}
		out = append(out, Result{
			SessionID:  iss.SessionID,
			PayloadID:  iss.PayloadID,
			Confidence: correlateSequence(soft, seq),
			BitsTotal:  len(soft),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// BookFor rebuilds the codebook an asset was packaged with.
func BookFor(meta storage.Meta) (*codebook.Codebook, error) {
	if !meta.Watermarked || meta.Codebook == nil {
		return nil, errors.New("detect: asset was not packaged with a watermark")
	}
	return codebook.New(*meta.Codebook)
}
