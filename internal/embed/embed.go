// Package embed writes and reads the invisible A/B segment watermark.
package embed

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const Version = 1

// Params must round-trip through meta.json unchanged: a detector rebuilds the
// exact grid an asset was packaged with, possibly years later.
type Params struct {
	Version int   `json:"version"`
	Width   int   `json:"width"`
	Height  int   `json:"height"`
	Block   int   `json:"block"`
	Seed    int64 `json:"seed"`
	// Amplitude is the luma offset in code values. Spike 2 measured that 2 is
	// recoverable only when the block grid is already aligned; once the detector
	// searches for a crop, the null floor rises and 4 is the practical minimum.
	Amplitude int `json:"amplitude"`
}

// MinBlocks is the smallest grid that detects reliably. Correlation noise falls
// as 1/sqrt(blocks): measured against real encodes, a 32x18 grid separates a
// mark from the decoy null by roughly 2.7x, while a 16x9 grid is drowned by it.
const MinBlocks = 400

// TargetGridWidth keeps the grid near 32x18 whatever the frame size, so
// detection quality does not silently depend on the source resolution.
const TargetGridWidth = 32

func DefaultParams(width, height int) Params {
	block := width / TargetGridWidth
	block -= block % 2
	if block < 8 {
		block = 8
	}
	return Params{
		Version:   Version,
		Width:     width,
		Height:    height,
		Block:     block,
		Seed:      1,
		Amplitude: 6,
	}
}

func (p Params) Validate() error {
	switch {
	case p.Version != Version:
		return fmt.Errorf("embed: unsupported params version %d, want %d", p.Version, Version)
	case p.Width <= 0 || p.Height <= 0:
		return errors.New("embed: width and height must be positive")
	case p.Block <= 0:
		return errors.New("embed: block size must be positive")
	case p.Width/p.Block*(p.Height/p.Block) < MinBlocks:
		return fmt.Errorf("embed: frame %dx%d with %d-pixel blocks gives a %dx%d grid, below the %d blocks detection needs",
			p.Width, p.Height, p.Block, p.Width/p.Block, p.Height/p.Block, MinBlocks)
	case p.Amplitude < 1 || p.Amplitude > 32:
		return fmt.Errorf("embed: amplitude %d is outside the usable range 1-32", p.Amplitude)
	}
	return nil
}

// Blocks floors the grid: real frame sizes are rarely a whole number of blocks,
// and the leftover right and bottom strip simply carries no mark.
func (p Params) Blocks() (int, int) { return p.Width / p.Block, p.Height / p.Block }

// Embedder writes a watermarked copy carrying one variant bit, and reads a soft
// decision back.
//
// Extract returns a confidence in [-1, 1] rather than a bit: sequence-level
// decoding needs the magnitude, and a hard per-segment threshold throws away
// the information that makes recovery work under noise.
type Embedder interface {
	Embed(ctx context.Context, src, dst string, variant uint8, p Params) error
	Extract(ctx context.Context, src string, p Params) (float64, error)
}

// Pattern is the block sign grid. Signs come from SHA-256 rather than math/rand
// so an asset packaged today stays detectable by any future build.
type Pattern struct {
	NX, NY int
	Sign   []int8
}

func NewPattern(p Params) (*Pattern, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	nx, ny := p.Blocks()
	pat := &Pattern{NX: nx, NY: ny, Sign: make([]int8, nx*ny)}

	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(p.Seed))
	for base := 0; base < len(pat.Sign); base += 256 {
		binary.LittleEndian.PutUint64(buf[8:], uint64(base/256))
		sum := sha256.Sum256(buf[:])
		for j := 0; j < 256 && base+j < len(pat.Sign); j++ {
			if (sum[j/8]>>(j%8))&1 == 1 {
				pat.Sign[base+j] = 1
			} else {
				pat.Sign[base+j] = -1
			}
		}
	}
	return pat, nil
}

func (p *Pattern) At(bx, by int) int8 { return p.Sign[by*p.NX+bx] }

// GrayPlane renders the pattern as an 8-bit plane centred on 128, which is what
// ffmpeg's grainmerge blend (out = a + b - 128) needs to add exactly
// amplitude*sign to luma.
func (p *Pattern) GrayPlane(params Params, variant uint8) ([]byte, error) {
	if variant > 1 {
		return nil, fmt.Errorf("embed: variant must be 0 or 1, got %d", variant)
	}
	polarity := 1
	if variant == 1 {
		polarity = -1
	}
	// 128 is neutral under grainmerge, so the ungridded remainder is untouched.
	buf := make([]byte, params.Width*params.Height)
	for i := range buf {
		buf[i] = 128
	}
	for y := range p.NY * params.Block {
		row := y * params.Width
		for x := range p.NX * params.Block {
			v := 128 + int(p.At(x/params.Block, y/params.Block))*polarity*params.Amplitude
			buf[row+x] = byte(clampInt(v, 0, 255))
		}
	}
	return buf, nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
