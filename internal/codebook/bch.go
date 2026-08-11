package codebook

import (
	"errors"
	"fmt"
)

var errUncorrectable = errors.New("codebook: more errors than the code can correct")

type bch struct {
	f   *field
	n   int
	k   int
	t   int
	gen []int
}

func newBCH(m, t int) (*bch, error) {
	f, err := newField(m)
	if err != nil {
		return nil, err
	}
	if t < 1 {
		return nil, fmt.Errorf("codebook: correction power must be at least 1, got %d", t)
	}
	if 2*t >= f.n {
		return nil, fmt.Errorf("codebook: t=%d is too large for a length-%d code", t, f.n)
	}

	gen := []int{1}
	seen := map[int]bool{}
	for i := 1; i <= 2*t; i++ {
		lead := f.cosetLeader(i)
		if seen[lead] {
			continue
		}
		seen[lead] = true
		gen = f.polyMul(gen, f.minimalPoly(lead))
	}

	k := f.n - (len(gen) - 1)
	if k < 1 {
		return nil, fmt.Errorf("codebook: BCH(%d, t=%d) leaves no payload bits", f.n, t)
	}
	return &bch{f: f, n: f.n, k: k, t: t, gen: gen}, nil
}

func (b *bch) encode(msg []uint8) ([]uint8, error) {
	if len(msg) != b.k {
		return nil, fmt.Errorf("codebook: message is %d bits, want %d", len(msg), b.k)
	}
	parityLen := b.n - b.k
	parity := make([]uint8, parityLen)
	for i := b.k - 1; i >= 0; i-- {
		fb := msg[i] ^ parity[parityLen-1]
		for j := parityLen - 1; j > 0; j-- {
			parity[j] = parity[j-1]
			if fb == 1 && b.gen[j] == 1 {
				parity[j] ^= 1
			}
		}
		parity[0] = 0
		if fb == 1 && b.gen[0] == 1 {
			parity[0] = 1
		}
	}

	code := make([]uint8, b.n)
	copy(code, parity)
	copy(code[parityLen:], msg)
	return code, nil
}

func (b *bch) syndromes(recv []uint8) ([]int, bool) {
	s := make([]int, 2*b.t)
	zero := true
	for j := 1; j <= 2*b.t; j++ {
		var acc int
		for i, bit := range recv {
			if bit == 1 {
				acc ^= b.f.exp[(j*i)%b.f.n]
			}
		}
		s[j-1] = acc
		if acc != 0 {
			zero = false
		}
	}
	return s, zero
}

// decode returns the recovered message, the number of bits corrected, and
// whether the received word was correctable.
func (b *bch) decode(recv []uint8) ([]uint8, int, error) {
	if len(recv) != b.n {
		return nil, 0, fmt.Errorf("codebook: received word is %d bits, want %d", len(recv), b.n)
	}
	synd, zero := b.syndromes(recv)
	if zero {
		return append([]uint8(nil), recv[b.n-b.k:]...), 0, nil
	}

	lambda := b.berlekampMassey(synd)
	positions := b.chien(lambda)
	if len(positions) != len(lambda)-1 {
		return nil, 0, errUncorrectable
	}

	corrected := append([]uint8(nil), recv...)
	for _, p := range positions {
		corrected[p] ^= 1
	}
	if _, ok := b.syndromes(corrected); !ok {
		return nil, 0, errUncorrectable
	}
	return corrected[b.n-b.k:], len(positions), nil
}

func (b *bch) berlekampMassey(s []int) []int {
	f := b.f
	c := []int{1}
	prev := []int{1}
	prevDisc := 1
	l, shift := 0, 1

	for r := range s {
		d := s[r]
		for i := 1; i <= l && i < len(c); i++ {
			if r-i >= 0 {
				d ^= f.mul(c[i], s[r-i])
			}
		}
		switch {
		case d == 0:
			shift++
		case 2*l <= r:
			saved := append([]int(nil), c...)
			c = polyShiftXor(f, c, prev, f.mul(d, f.inv(prevDisc)), shift)
			l = r + 1 - l
			prev, prevDisc, shift = saved, d, 1
		default:
			c = polyShiftXor(f, c, prev, f.mul(d, f.inv(prevDisc)), shift)
			shift++
		}
	}
	return c
}

// chien reports the error positions, which are the exponents whose inverse is
// a root of the error locator.
func (b *bch) chien(lambda []int) []int {
	var pos []int
	for i := range b.n {
		x := b.f.exp[(b.f.n-i%b.f.n)%b.f.n]
		if polyEval(b.f, lambda, x) == 0 {
			pos = append(pos, i)
		}
	}
	return pos
}

func polyShiftXor(f *field, c, prev []int, coef, shift int) []int {
	size := max(len(c), len(prev)+shift)
	out := make([]int, size)
	copy(out, c)
	for i, v := range prev {
		out[i+shift] ^= f.mul(coef, v)
	}
	return trimPoly(out)
}

func trimPoly(p []int) []int {
	last := len(p) - 1
	for last > 0 && p[last] == 0 {
		last--
	}
	return p[:last+1]
}
