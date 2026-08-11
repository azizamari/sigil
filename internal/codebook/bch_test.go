package codebook

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestBCHParameters(t *testing.T) {
	tests := []struct {
		m, t, wantN, wantK int
	}{
		{m: 4, t: 1, wantN: 15, wantK: 11},
		{m: 4, t: 2, wantN: 15, wantK: 7},
		{m: 4, t: 3, wantN: 15, wantK: 5},
		{m: 5, t: 1, wantN: 31, wantK: 26},
		{m: 5, t: 3, wantN: 31, wantK: 16},
		{m: 6, t: 1, wantN: 63, wantK: 57},
		{m: 6, t: 3, wantN: 63, wantK: 45},
		{m: 6, t: 5, wantN: 63, wantK: 36},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("m%d_t%d", tt.m, tt.t), func(t *testing.T) {
			b, err := newBCH(tt.m, tt.t)
			if err != nil {
				t.Fatalf("newBCH(%d, %d): %v", tt.m, tt.t, err)
			}
			if b.n != tt.wantN || b.k != tt.wantK {
				t.Errorf("newBCH(%d, %d) = BCH(%d,%d), want BCH(%d,%d)",
					tt.m, tt.t, b.n, b.k, tt.wantN, tt.wantK)
			}
		})
	}
}

func TestBCHRejectsBadParameters(t *testing.T) {
	tests := []struct {
		name string
		m, t int
	}{
		{"unsupported field", 9, 2},
		{"zero correction", 5, 0},
		{"t exceeds length", 4, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newBCH(tt.m, tt.t); err == nil {
				t.Fatalf("newBCH(%d, %d) = nil error, want error", tt.m, tt.t)
			}
		})
	}
}

func TestBCHEncodeIsSystematic(t *testing.T) {
	b, _ := newBCH(6, 3)
	msg := randomBits(b.k, rand.New(rand.NewSource(1)))
	code, err := b.encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for i, want := range msg {
		if got := code[b.n-b.k+i]; got != want {
			t.Fatalf("message bit %d = %d in codeword, want %d", i, got, want)
		}
	}
	if _, zero := b.syndromes(code); !zero {
		t.Fatal("valid codeword has nonzero syndromes")
	}
}

func TestBCHEncodeRejectsWrongLength(t *testing.T) {
	b, _ := newBCH(5, 2)
	if _, err := b.encode(make([]uint8, b.k+1)); err == nil {
		t.Fatal("encode with oversized message = nil error, want error")
	}
	if _, _, err := b.decode(make([]uint8, b.n-1)); err == nil {
		t.Fatal("decode with short word = nil error, want error")
	}
}

func TestBCHCorrectsUpToT(t *testing.T) {
	for _, cfg := range []struct{ m, t int }{{4, 2}, {5, 3}, {6, 5}} {
		b, err := newBCH(cfg.m, cfg.t)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(fmt.Sprintf("m%d_t%d", cfg.m, cfg.t), func(t *testing.T) {
			r := rand.New(rand.NewSource(int64(cfg.m*100 + cfg.t)))
			for nerr := 0; nerr <= b.t; nerr++ {
				for trial := 0; trial < 200; trial++ {
					msg := randomBits(b.k, r)
					code, err := b.encode(msg)
					if err != nil {
						t.Fatal(err)
					}
					got, corrected, err := b.decode(flipRandom(code, nerr, r))
					if err != nil {
						t.Fatalf("%d errors: decode failed: %v", nerr, err)
					}
					if corrected != nerr {
						t.Fatalf("%d errors: corrected %d", nerr, corrected)
					}
					if !equalBits(got, msg) {
						t.Fatalf("%d errors: decode returned the wrong message", nerr)
					}
				}
			}
		})
	}
}

// Beyond the correction bound a BCH decoder may fail or miscorrect; what it
// must never do is panic or report success while returning a corrupt message.
func TestBCHBeyondBoundDoesNotClaimFalseSuccess(t *testing.T) {
	b, _ := newBCH(6, 3)
	r := rand.New(rand.NewSource(7))
	for trial := 0; trial < 500; trial++ {
		msg := randomBits(b.k, r)
		code, _ := b.encode(msg)
		got, corrected, err := b.decode(flipRandom(code, b.t+1+r.Intn(5), r))
		if err != nil {
			continue
		}
		if corrected <= b.t && equalBits(got, msg) {
			t.Fatal("recovered the message from more errors than t, which is impossible")
		}
	}
}

func randomBits(n int, r *rand.Rand) []uint8 {
	bits := make([]uint8, n)
	for i := range bits {
		bits[i] = uint8(r.Intn(2))
	}
	return bits
}

func flipRandom(code []uint8, n int, r *rand.Rand) []uint8 {
	out := append([]uint8(nil), code...)
	for _, p := range r.Perm(len(out))[:n] {
		out[p] ^= 1
	}
	return out
}

func equalBits(a, b []uint8) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
