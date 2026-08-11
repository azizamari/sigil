package codebook

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Version tracks the layout of an assigned sequence. Assets packaged under an
// older version must stay detectable, so it is written to meta.json and any
// change to assignment or ECC must bump it.
const Version = 1

var ErrNoFit = errors.New("codebook: no code satisfies the payload and window constraints")

type Params struct {
	Version      int
	M            int
	T            int
	SegmentCount int
	Seed         int64
}

type Codebook struct {
	params Params
	code   *bch
	mask   []uint8
}

func New(p Params) (*Codebook, error) {
	if p.Version != Version {
		return nil, fmt.Errorf("codebook: unsupported version %d, want %d", p.Version, Version)
	}
	code, err := newBCH(p.M, p.T)
	if err != nil {
		return nil, err
	}
	if p.SegmentCount < code.n {
		return nil, fmt.Errorf("codebook: %d segments cannot carry a %d-bit codeword",
			p.SegmentCount, code.n)
	}
	return &Codebook{params: p, code: code, mask: whitening(p.Seed, p.SegmentCount)}, nil
}

// BCH is cyclic, so every rotation of a codeword is another valid codeword: a
// tiled sequence read at the wrong offset would decode cleanly to the wrong id.
// XOR-ing a position-dependent mask over the tiling breaks that symmetry, so
// only the true offset de-whitens into a codeword.
//
// SHA-256 rather than math/rand because this mask is persisted in meta.json and
// must reproduce identically for as long as any packaged asset exists.
func whitening(seed int64, n int) []uint8 {
	mask := make([]uint8, n)
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(seed))
	for base := 0; base < n; base += 256 {
		binary.LittleEndian.PutUint64(buf[8:], uint64(base/256))
		sum := sha256.Sum256(buf[:])
		for j := 0; j < 256 && base+j < n; j++ {
			mask[base+j] = (sum[j/8] >> (j % 8)) & 1
		}
	}
	return mask
}

// Fit chooses the most error-tolerant code that carries payloadBits and still
// fits inside a window of windowSegments, which is what makes an identifier
// recoverable from a short clip rather than only from the whole asset.
func Fit(payloadBits, windowSegments, segmentCount int) (Params, error) {
	best := Params{Version: Version, SegmentCount: segmentCount}
	found := false
	for m := 3; m <= 8; m++ {
		n := 1<<m - 1
		if 3*n > windowSegments || n > segmentCount {
			continue
		}
		for t := 1; 2*t < n; t++ {
			code, err := newBCH(m, t)
			if err != nil || code.k < payloadBits {
				continue
			}
			if !found || t > best.T || (t == best.T && n > 1<<best.M-1) {
				best.M, best.T, found = m, t, true
			}
		}
	}
	if !found {
		return Params{}, fmt.Errorf("%w: %d payload bits need a codeword whose confident window exceeds %d segments; use shorter segments or fewer identities",
			ErrNoFit, payloadBits, windowSegments)
	}
	return best, nil
}

func (c *Codebook) Params() Params    { return c.params }
func (c *Codebook) PayloadBits() int  { return c.code.k }
func (c *Codebook) CodewordLen() int  { return c.code.n }
func (c *Codebook) Corrects() int     { return c.code.t }
func (c *Codebook) Capacity() uint64  { return 1 << uint(c.code.k) }
func (c *Codebook) SegmentCount() int { return c.params.SegmentCount }

// MinWindow is the shortest window from which an id can be recovered at all.
//
// A window of exactly one codeword cannot work, however tempting the arithmetic
// looks: with any bit errors present, some wrong offset always decodes to a
// wrong id that fits the data just as well, and unwatermarked noise scores a
// median of 0.96 because every noise vector lands in some codeword's decoding
// sphere. Half a codeword of slack removes the ambiguity.
func (c *Codebook) MinWindow() int { return (3*c.code.n + 1) / 2 }

// ConfidentWindow is the shortest window at which genuine matches separate from
// the null distribution, measured rather than derived. Recovery works from
// MinWindow, but between the two an attribution is not evidence of anything:
// unwatermarked input still scores high enough to overlap a real match.
func (c *Codebook) ConfidentWindow() int { return 3 * c.code.n }

// Sequence returns the variant bit for every segment of the asset. The codeword
// is tiled rather than laid out once, so any window of MinWindow segments
// carries a complete identifier at some rotation.
func (c *Codebook) Sequence(id uint64) ([]uint8, error) {
	if c.code.k < 64 && id >= 1<<uint(c.code.k) {
		return nil, fmt.Errorf("codebook: id %d exceeds the %d-bit payload", id, c.code.k)
	}
	msg := make([]uint8, c.code.k)
	for i := range msg {
		msg[i] = uint8((id >> uint(i)) & 1)
	}
	word, err := c.code.encode(msg)
	if err != nil {
		return nil, err
	}
	seq := make([]uint8, c.params.SegmentCount)
	for i := range seq {
		seq[i] = word[i%c.code.n] ^ c.mask[i]
	}
	return seq, nil
}

type Match struct {
	ID     uint64
	Offset int
	Errors int
	Score  float64
}

// Decode recovers an identifier from per-segment soft decisions in [-1, 1],
// where positive favours variant 1. The window may start at any segment of the
// asset, so every start is tried and the best-correlating candidate wins.
func (c *Codebook) Decode(soft []float64) (Match, error) {
	switch {
	case len(soft) < c.MinWindow():
		return Match{}, fmt.Errorf("codebook: need at least %d soft decisions, got %d",
			c.MinWindow(), len(soft))
	case len(soft) > c.params.SegmentCount:
		return Match{}, fmt.Errorf("codebook: window of %d exceeds the %d-segment asset",
			len(soft), c.params.SegmentCount)
	}
	return c.search(soft)
}

func (c *Codebook) search(soft []float64) (Match, error) {
	n := c.code.n
	best := Match{Score: math.Inf(-1)}
	found := false
	for start := 0; start+len(soft) <= c.params.SegmentCount; start++ {
		folded := make([]float64, n)
		for j, v := range soft {
			if c.mask[start+j] == 1 {
				v = -v
			}
			folded[(start+j)%n] += v
		}
		msg, errs, ok := c.chase(folded)
		if !ok {
			continue
		}
		word, err := c.code.encode(msg)
		if err != nil {
			return Match{}, err
		}
		if score := c.correlateAt(soft, word, start); score > best.Score {
			best = Match{ID: bitsToID(msg), Offset: start, Errors: errs, Score: score}
			found = true
		}
	}
	if !found {
		return Match{}, errUncorrectable
	}
	return best, nil
}

func (c *Codebook) correlateAt(soft []float64, word []uint8, start int) float64 {
	var num, den float64
	for j, v := range soft {
		sign := -1.0
		if word[(start+j)%len(word)]^c.mask[start+j] == 1 {
			sign = 1
		}
		num += v * sign
		den += math.Abs(v)
	}
	if den == 0 {
		return 0
	}
	return num / den
}

// chaseFlips is the number of least-reliable positions Chase-II retries. Four
// costs 16 decodes per rotation and recovers words a hard decision loses.
const chaseFlips = 4

func (c *Codebook) chase(folded []float64) ([]uint8, int, bool) {
	hard := make([]uint8, len(folded))
	for i, v := range folded {
		if v > 0 {
			hard[i] = 1
		}
	}
	// An error-free decode correlates at exactly 1.0, so no flip pattern can beat
	// it and the remaining trials are wasted work.
	if msg, errs, err := c.code.decode(hard); err == nil && errs == 0 {
		return msg, 0, true
	}
	weak := leastReliable(folded, chaseFlips)

	var bestMsg []uint8
	bestScore := math.Inf(-1)
	bestErrs := 0
	for pattern := range 1 << len(weak) {
		trial := append([]uint8(nil), hard...)
		for i, pos := range weak {
			if pattern&(1<<i) != 0 {
				trial[pos] ^= 1
			}
		}
		msg, errs, err := c.code.decode(trial)
		if err != nil {
			continue
		}
		word, err := c.code.encode(msg)
		if err != nil {
			continue
		}
		if s := correlate(folded, word); s > bestScore {
			bestMsg, bestScore, bestErrs = msg, s, errs
		}
	}
	return bestMsg, bestErrs, bestMsg != nil
}

func leastReliable(v []float64, count int) []int {
	if count > len(v) {
		count = len(v)
	}
	idx := make([]int, len(v))
	for i := range idx {
		idx[i] = i
	}
	for i := range count {
		minAt := i
		for j := i + 1; j < len(idx); j++ {
			if math.Abs(v[idx[j]]) < math.Abs(v[idx[minAt]]) {
				minAt = j
			}
		}
		idx[i], idx[minAt] = idx[minAt], idx[i]
	}
	return idx[:count]
}

// correlate scores a codeword against de-whitened soft decisions, normalised
// to [-1, 1] so windows of different lengths stay comparable.
func correlate(soft []float64, word []uint8) float64 {
	var num, den float64
	for j, v := range soft {
		sign := -1.0
		if word[j%len(word)] == 1 {
			sign = 1
		}
		num += v * sign
		den += math.Abs(v)
	}
	if den == 0 {
		return 0
	}
	return num / den
}

func bitsToID(msg []uint8) uint64 {
	var id uint64
	for i, b := range msg {
		if b == 1 {
			id |= 1 << uint(i)
		}
	}
	return id
}
