package main

import "math"

func blockMeans(frame []byte, p *Pattern) []float64 {
	m := make([]float64, p.NX*p.NY)
	for y := 0; y < p.H; y++ {
		by := y / p.Block
		row := y * p.W
		for x := 0; x < p.W; x++ {
			m[by*p.NX+x/p.Block] += float64(frame[row+x])
		}
	}
	area := float64(p.Block * p.Block)
	for i := range m {
		m[i] /= area
	}
	return m
}

// Subtracting the 8-neighbour mean removes scene content, which is spatially
// smooth at block scale, while leaving the watermark: its neighbouring signs
// are independent so they average to roughly zero.
func score(frame []byte, p *Pattern) float64 {
	m := blockMeans(frame, p)
	var sum float64
	var n int
	for by := 1; by < p.NY-1; by++ {
		for bx := 1; bx < p.NX-1; bx++ {
			var around float64
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if dx != 0 || dy != 0 {
						around += m[(by+dy)*p.NX+bx+dx]
					}
				}
			}
			sum += (m[by*p.NX+bx] - around/8) * float64(p.At(bx, by))
			n++
		}
	}
	return sum / float64(n)
}

func mean(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func stddev(xs []float64) float64 {
	m := mean(xs)
	var s float64
	for _, x := range xs {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(xs)))
}

// windowDecisions reports the fraction of clip-length windows whose mean score
// has the expected sign, which is the number that matters: detection must work
// from a short window, not the whole asset.
func windowAccuracy(scores []float64, window int, wantPositive bool) float64 {
	if len(scores) < window {
		return math.NaN()
	}
	var ok, total int
	for i := 0; i+window <= len(scores); i += window {
		s := mean(scores[i : i+window])
		if (s > 0) == wantPositive {
			ok++
		}
		total++
	}
	return float64(ok) / float64(total)
}
