package main

import (
	"fmt"
	"math/rand"
	"os"
)

// Modulation is block-constant rather than per-pixel because an encoder
// discards high-frequency detail first; a block's mean survives requantisation.
type Pattern struct {
	W, H   int
	Block  int
	NX, NY int
	Sign   []int8
}

func NewPattern(w, h, block int, seed int64) *Pattern {
	if w%block != 0 || h%block != 0 {
		panic(fmt.Sprintf("frame %dx%d is not a whole number of %d-pixel blocks", w, h, block))
	}
	p := &Pattern{W: w, H: h, Block: block, NX: w / block, NY: h / block}
	p.Sign = make([]int8, p.NX*p.NY)
	r := rand.New(rand.NewSource(seed))
	for i := range p.Sign {
		if r.Intn(2) == 0 {
			p.Sign[i] = -1
		} else {
			p.Sign[i] = 1
		}
	}
	return p
}

func (p *Pattern) At(bx, by int) int8 { return p.Sign[by*p.NX+bx] }

// Centring on 128 makes ffmpeg's grainmerge blend (out = a + b - 128) add
// exactly amp*sign*polarity to luma.
func (p *Pattern) WriteGrayPlane(path string, amp int, polarity int8) error {
	buf := make([]byte, p.W*p.H)
	for y := 0; y < p.H; y++ {
		row := y * p.W
		for x := 0; x < p.W; x++ {
			v := 128 + int(p.At(x/p.Block, y/p.Block))*int(polarity)*amp
			buf[row+x] = byte(clamp(v, 0, 255))
		}
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return fmt.Errorf("write gray plane %s: %w", path, err)
	}
	return nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
