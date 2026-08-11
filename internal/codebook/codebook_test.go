package codebook

import (
	"errors"
	"math/rand"
	"sort"
	"testing"
)

func testbook(t *testing.T, m, tt, segments int) *Codebook {
	t.Helper()
	c, err := New(Params{Version: Version, M: m, T: tt, SegmentCount: segments})
	if err != nil {
		t.Fatalf("New(m=%d t=%d seg=%d): %v", m, tt, segments, err)
	}
	return c
}

func TestNewRejectsBadParams(t *testing.T) {
	tests := []struct {
		name string
		p    Params
	}{
		{"wrong version", Params{Version: 99, M: 5, T: 2, SegmentCount: 100}},
		{"unsupported field", Params{Version: Version, M: 9, T: 2, SegmentCount: 100}},
		{"segments below codeword", Params{Version: Version, M: 6, T: 3, SegmentCount: 10}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.p); err == nil {
				t.Fatal("New = nil error, want error")
			}
		})
	}
}

func TestSequenceRoundTrip(t *testing.T) {
	c := testbook(t, 5, 3, 300)
	for _, id := range []uint64{0, 1, 42, 1023, c.Capacity() - 1} {
		seq, err := c.Sequence(id)
		if err != nil {
			t.Fatalf("Sequence(%d): %v", id, err)
		}
		if len(seq) != 300 {
			t.Fatalf("Sequence(%d) length = %d, want 300", id, len(seq))
		}
		got, err := c.Decode(bitsToSoft(seq, 1))
		if err != nil {
			t.Fatalf("Decode(%d): %v", id, err)
		}
		if got.ID != id {
			t.Errorf("round trip = %d, want %d", got.ID, id)
		}
		if got.Score != 1 {
			t.Errorf("clean decode score = %v, want 1", got.Score)
		}
	}
}

func TestSequenceRejectsOversizedID(t *testing.T) {
	c := testbook(t, 4, 2, 100)
	if _, err := c.Sequence(c.Capacity()); err == nil {
		t.Fatal("Sequence with an out-of-range id = nil error, want error")
	}
}

func TestSequenceIsTiled(t *testing.T) {
	c := testbook(t, 5, 3, 300)
	seq, err := c.Sequence(1234)
	if err != nil {
		t.Fatal(err)
	}
	for i := range seq {
		bare := seq[i] ^ c.mask[i]
		if want := seq[i%c.CodewordLen()] ^ c.mask[i%c.CodewordLen()]; bare != want {
			t.Fatalf("segment %d breaks the tiling under the whitening mask", i)
		}
	}
}

// SPEC 6: the identifier must be recoverable from any window of the asset, not
// only from a window that happens to start on a codeword boundary.
func TestAnyWindowRecoversID(t *testing.T) {
	c := testbook(t, 5, 3, 300)
	const id = 9999
	seq, err := c.Sequence(id)
	if err != nil {
		t.Fatal(err)
	}
	window := c.MinWindow()
	for start := 0; start+window <= len(seq); start++ {
		got, err := c.Decode(bitsToSoft(seq[start:start+window], 1))
		if err != nil {
			t.Fatalf("window at %d: %v", start, err)
		}
		if got.ID != id {
			t.Fatalf("window at %d recovered %d, want %d", start, got.ID, id)
		}
		if got.Offset != start {
			t.Fatalf("window at %d reported offset %d", start, got.Offset)
		}
	}
}

func TestDecodeRejectsShortWindow(t *testing.T) {
	c := testbook(t, 5, 3, 300)
	if _, err := c.Decode(make([]float64, c.MinWindow()-1)); err == nil {
		t.Fatal("Decode with a short window = nil error, want error")
	}
}

// Property: for random ids and random flips within the correction bound,
// decoding always returns the original id.
func TestDecodeWithinCorrectionBound(t *testing.T) {
	c := testbook(t, 5, 3, 300)
	r := rand.New(rand.NewSource(11))
	window := c.MinWindow()
	for trial := 0; trial < 200; trial++ {
		id := uint64(r.Intn(int(c.Capacity())))
		seq, err := c.Sequence(id)
		if err != nil {
			t.Fatal(err)
		}
		start := r.Intn(len(seq) - window)
		soft := bitsToSoft(seq[start:start+window], 1)
		for _, p := range r.Perm(window)[:r.Intn(c.Corrects()+1)] {
			soft[p] = -soft[p]
		}
		got, err := c.Decode(soft)
		if err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
		if got.ID != id {
			t.Fatalf("trial %d: recovered %d, want %d", trial, got.ID, id)
		}
	}
}

// The detector is fed confidences, not bits: a wrong sign carrying low
// confidence must lose to the many weak-but-correct decisions around it.
func TestDecodeFromNoisySoftDecisions(t *testing.T) {
	c := testbook(t, 5, 3, 300)
	r := rand.New(rand.NewSource(23))
	var recovered int
	const trials = 200
	for range trials {
		id := uint64(r.Intn(int(c.Capacity())))
		seq, _ := c.Sequence(id)
		soft := make([]float64, c.ConfidentWindow())
		for i := range soft {
			v := 0.35
			if seq[i] == 0 {
				v = -0.35
			}
			soft[i] = v + r.NormFloat64()*0.35
		}
		if got, err := c.Decode(soft); err == nil && got.ID == id {
			recovered++
		}
	}
	if rate := float64(recovered) / trials; rate < 0.95 {
		t.Errorf("recovery rate %.2f from noisy soft decisions, want >= 0.95", rate)
	}
}

// SPEC 10: detection must be run against unwatermarked content and return no
// match. Nothing stops the decoder finding *an* id in noise, so the only thing
// that separates evidence from an accusation generator is the score, and the
// threshold has to come from a measured null distribution.
func TestNullSeparatesFromGenuineAtConfidentWindow(t *testing.T) {
	c := testbook(t, 5, 3, 150)

	nullScores := scoreNull(c, c.ConfidentWindow(), 300, 3)
	genuine := scoreGenuine(t, c, c.ConfidentWindow(), 200, 4, 0.35)

	nullMax := nullScores[len(nullScores)-1]
	genuineP5 := genuine[len(genuine)*5/100]
	if nullMax >= genuineP5 {
		t.Errorf("null max %.3f overlaps genuine p5 %.3f: no usable threshold exists",
			nullMax, genuineP5)
	}
}

// Executable documentation for why MinWindow carries margin over a codeword.
func TestNullIsUselessAtOneCodeword(t *testing.T) {
	c := testbook(t, 5, 3, 150)
	scores := scoreNullSearch(c, c.CodewordLen(), 200, 3)
	if median := scores[len(scores)/2]; median < 0.9 {
		t.Errorf("null median at one codeword = %.3f; the margin in MinWindow may no longer be needed", median)
	}
}

// scoreNullSearch bypasses the MinWindow guard to show what that guard prevents.
func scoreNullSearch(c *Codebook, window, trials int, seed int64) []float64 {
	r := rand.New(rand.NewSource(seed))
	var scores []float64
	for range trials {
		soft := make([]float64, window)
		for i := range soft {
			soft[i] = r.NormFloat64() * 0.5
		}
		if got, err := c.search(soft); err == nil {
			scores = append(scores, got.Score)
		}
	}
	sort.Float64s(scores)
	return scores
}

func scoreNull(c *Codebook, window, trials int, seed int64) []float64 {
	r := rand.New(rand.NewSource(seed))
	var scores []float64
	for range trials {
		soft := make([]float64, window)
		for i := range soft {
			soft[i] = r.NormFloat64() * 0.5
		}
		if got, err := c.Decode(soft); err == nil {
			scores = append(scores, got.Score)
		}
	}
	sort.Float64s(scores)
	return scores
}

func scoreGenuine(t *testing.T, c *Codebook, window, trials int, seed int64, noise float64) []float64 {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	var scores []float64
	for range trials {
		id := uint64(r.Intn(int(c.Capacity())))
		seq, err := c.Sequence(id)
		if err != nil {
			t.Fatal(err)
		}
		soft := make([]float64, window)
		for i := range soft {
			v := 0.35
			if seq[i] == 0 {
				v = -0.35
			}
			soft[i] = v + r.NormFloat64()*noise
		}
		if got, err := c.Decode(soft); err == nil && got.ID == id {
			scores = append(scores, got.Score)
		}
	}
	sort.Float64s(scores)
	return scores
}

func TestFitPicksMostTolerantCode(t *testing.T) {
	tests := []struct {
		name                   string
		payload, window, total int
		wantM, wantT           int
		wantErr                bool
	}{
		{name: "1s segments carry 16 bits", payload: 14, window: 93, total: 900, wantM: 5, wantT: 3},
		{name: "long window allows a larger field", payload: 14, window: 200, total: 900, wantM: 6, wantT: 11},
		{name: "4s segments cannot carry 10k sessions", payload: 14, window: 22, total: 300, wantErr: true},
		{name: "2s segments cannot either", payload: 14, window: 45, total: 450, wantErr: true},
		{name: "short window with small payload", payload: 8, window: 45, total: 300, wantM: 4, wantT: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Fit(tc.payload, tc.window, tc.total)
			if tc.wantErr {
				if !errors.Is(err, ErrNoFit) {
					t.Fatalf("Fit = %v, want ErrNoFit", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Fit: %v", err)
			}
			if p.M != tc.wantM || p.T != tc.wantT {
				t.Errorf("Fit = m%d t%d, want m%d t%d", p.M, p.T, tc.wantM, tc.wantT)
			}
			c, err := New(p)
			if err != nil {
				t.Fatalf("New from fitted params: %v", err)
			}
			if c.PayloadBits() < tc.payload {
				t.Errorf("fitted payload %d bits, want at least %d", c.PayloadBits(), tc.payload)
			}
			if c.ConfidentWindow() > tc.window {
				t.Errorf("fitted confident window %d exceeds %d", c.ConfidentWindow(), tc.window)
			}
		})
	}
}

func bitsToSoft(bits []uint8, magnitude float64) []float64 {
	soft := make([]float64, len(bits))
	for i, b := range bits {
		if b == 1 {
			soft[i] = magnitude
		} else {
			soft[i] = -magnitude
		}
	}
	return soft
}
