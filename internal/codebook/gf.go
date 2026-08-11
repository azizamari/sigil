package codebook

import "fmt"

// Bit i of each mask is the coefficient of x^i.
var primitivePolys = map[int]int{
	3: 0b1011,
	4: 0b10011,
	5: 0b100101,
	6: 0b1000011,
	7: 0b10001001,
	8: 0b100011101,
}

type field struct {
	m   int
	n   int
	exp []int
	log []int
}

func newField(m int) (*field, error) {
	prim, ok := primitivePolys[m]
	if !ok {
		return nil, fmt.Errorf("codebook: no primitive polynomial for GF(2^%d)", m)
	}
	n := 1<<m - 1
	f := &field{m: m, n: n, exp: make([]int, 2*n), log: make([]int, n+1)}
	f.log[0] = -1
	x := 1
	for i := range n {
		f.exp[i] = x
		f.log[x] = i
		x <<= 1
		if x&(1<<m) != 0 {
			x ^= prim
		}
	}
	// Doubling the exp table lets mul index log[a]+log[b] without a modulo.
	for i := n; i < 2*n; i++ {
		f.exp[i] = f.exp[i-n]
	}
	return f, nil
}

func (f *field) mul(a, b int) int {
	if a == 0 || b == 0 {
		return 0
	}
	return f.exp[f.log[a]+f.log[b]]
}

func (f *field) inv(a int) int {
	if a == 0 {
		panic("codebook: inverse of zero")
	}
	if f.log[a] == 0 {
		return 1
	}
	return f.exp[f.n-f.log[a]]
}

func (f *field) pow(a, e int) int {
	if a == 0 {
		return 0
	}
	return f.exp[(f.log[a]*e)%f.n]
}

// polyMul multiplies polynomials whose coefficients are field elements,
// indexed by degree.
func (f *field) polyMul(a, b []int) []int {
	out := make([]int, len(a)+len(b)-1)
	for i, av := range a {
		if av == 0 {
			continue
		}
		for j, bv := range b {
			out[i+j] ^= f.mul(av, bv)
		}
	}
	return out
}

// minimalPoly returns the minimal polynomial over GF(2) of α^i, built from the
// cyclotomic coset of i so that its coefficients collapse to 0 or 1.
func (f *field) minimalPoly(i int) []int {
	seen := map[int]bool{}
	p := []int{1}
	for e := i; !seen[e]; e = e * 2 % f.n {
		seen[e] = true
		p = f.polyMul(p, []int{f.exp[e], 1})
	}
	return p
}

// cosetLeader identifies the cyclotomic coset of i so duplicate minimal
// polynomials are multiplied into the generator only once.
func (f *field) cosetLeader(i int) int {
	min := i
	for e := i * 2 % f.n; e != i; e = e * 2 % f.n {
		if e < min {
			min = e
		}
	}
	return min
}

func polyEval(f *field, p []int, x int) int {
	var acc int
	for d := len(p) - 1; d >= 0; d-- {
		acc = f.mul(acc, x) ^ p[d]
	}
	return acc
}
