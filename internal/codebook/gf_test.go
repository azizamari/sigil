package codebook

import "testing"

func TestFieldArithmetic(t *testing.T) {
	for m := 3; m <= 8; m++ {
		f, err := newField(m)
		if err != nil {
			t.Fatalf("newField(%d): %v", m, err)
		}
		t.Run(fieldName(m), func(t *testing.T) {
			for a := 1; a <= f.n; a++ {
				if got := f.mul(a, f.inv(a)); got != 1 {
					t.Fatalf("mul(%d, inv(%d)) = %d, want 1", a, a, got)
				}
				if got := f.mul(a, 0); got != 0 {
					t.Fatalf("mul(%d, 0) = %d, want 0", a, got)
				}
				if got := f.pow(a, f.n); got != 1 {
					t.Fatalf("pow(%d, %d) = %d, want 1", a, f.n, got)
				}
			}
			for a := 1; a <= f.n; a++ {
				for b := 1; b <= f.n; b++ {
					if f.mul(a, b) != f.mul(b, a) {
						t.Fatalf("mul not commutative at (%d,%d)", a, b)
					}
				}
			}
		})
	}
}

func TestFieldGeneratorIsPrimitive(t *testing.T) {
	for m := 3; m <= 8; m++ {
		f, _ := newField(m)
		seen := make(map[int]bool, f.n)
		for i := range f.n {
			if seen[f.exp[i]] {
				t.Fatalf("GF(2^%d): α^%d repeats before the full cycle", m, i)
			}
			seen[f.exp[i]] = true
		}
		if len(seen) != f.n {
			t.Fatalf("GF(2^%d): %d distinct nonzero elements, want %d", m, len(seen), f.n)
		}
	}
}

func TestMinimalPolyIsBinary(t *testing.T) {
	for m := 3; m <= 6; m++ {
		f, _ := newField(m)
		for i := 1; i < f.n; i++ {
			p := f.minimalPoly(i)
			for d, c := range p {
				if c != 0 && c != 1 {
					t.Fatalf("GF(2^%d) minimalPoly(%d) coefficient x^%d = %d, want 0 or 1", m, i, d, c)
				}
			}
			if got := polyEval(f, p, f.exp[i]); got != 0 {
				t.Fatalf("GF(2^%d) minimalPoly(%d) does not vanish at α^%d: %d", m, i, i, got)
			}
		}
	}
}

func TestUnsupportedField(t *testing.T) {
	if _, err := newField(9); err == nil {
		t.Fatal("newField(9) = nil error, want error")
	}
}

func fieldName(m int) string {
	return "GF(2^" + string(rune('0'+m)) + ")"
}
