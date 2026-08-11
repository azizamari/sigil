package detect

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/azizamari/sigil/internal/codebook"
	"github.com/azizamari/sigil/internal/storage"
)

func testBook(t *testing.T) *codebook.Codebook {
	t.Helper()
	book, err := codebook.New(codebook.Params{
		Version: codebook.Version, M: 5, T: 3, SegmentCount: 300, Seed: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	return book
}

// issuedSet mints n sessions, reserving the payload under test for ses_target
// so the expected answer is unambiguous.
func issuedSet(n int, target uint64) []Issued {
	out := make([]Issued, 0, n+1)
	for i := range n {
		if uint64(i) == target {
			continue
		}
		out = append(out, Issued{SessionID: fmt.Sprintf("ses_%03d", i), PayloadID: uint64(i)})
	}
	return append(out, Issued{SessionID: "ses_target", PayloadID: target})
}

// softFor renders a session's sequence as soft decisions with injected noise,
// which is how a real extractor would present a degraded leak.
func softFor(t *testing.T, book *codebook.Codebook, payload uint64, start, length int, magnitude, noise float64, seed int64) SoftSequence {
	t.Helper()
	seq, err := book.Sequence(payload)
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(seed))
	soft := make(SoftSequence, length)
	for i := range soft {
		v := magnitude
		if seq[start+i] == 0 {
			v = -magnitude
		}
		soft[i] = v + r.NormFloat64()*noise
	}
	return soft
}

func TestAttributeIdentifiesTheIssuedSession(t *testing.T) {
	book := testBook(t)
	const payload = 42
	issued := issuedSet(40, payload)
	soft := softFor(t, book, payload, 0, book.ConfidentWindow(), 0.8, 0.2, 1)

	res, err := Attribute(book, soft, issued, 0)
	if err != nil {
		t.Fatalf("Attribute: %v", err)
	}
	if !res.Matched {
		t.Fatalf("clean leak was not matched: %+v", res)
	}
	if res.SessionID != "ses_target" {
		t.Errorf("attributed to %q, want ses_target", res.SessionID)
	}
	if res.Confidence <= res.NullPeak {
		t.Errorf("confidence %.3f does not exceed the null peak %.3f", res.Confidence, res.NullPeak)
	}
}

func TestAttributeWorksFromAnyPartOfTheAsset(t *testing.T) {
	book := testBook(t)
	const payload = 17
	issued := issuedSet(40, payload)

	for _, start := range []int{0, 31, 77, 150} {
		soft := softFor(t, book, payload, start, book.ConfidentWindow(), 0.8, 0.2, int64(start))
		res, err := Attribute(book, soft, issued, 0)
		if err != nil {
			t.Fatalf("start %d: %v", start, err)
		}
		if !res.Matched || res.SessionID != "ses_target" {
			t.Errorf("start %d: matched=%v session=%q", start, res.Matched, res.SessionID)
		}
	}
}

// SPEC 10: run detection against unwatermarked content and confirm no match.
// Without this the matcher happily returns its closest candidate for any input.
func TestUnwatermarkedInputDoesNotMatch(t *testing.T) {
	book := testBook(t)
	issued := issuedSet(40, 0)
	r := rand.New(rand.NewSource(99))

	var falsePositives int
	const trials = 50
	for range trials {
		soft := make(SoftSequence, book.ConfidentWindow())
		for i := range soft {
			soft[i] = r.NormFloat64() * 0.5
		}
		res, err := Attribute(book, soft, issued, 0)
		if err != nil {
			continue
		}
		if res.Matched {
			falsePositives++
			t.Logf("noise matched %s at confidence %.3f (null %.3f)", res.SessionID, res.Confidence, res.NullPeak)
		}
	}
	if falsePositives > 0 {
		t.Errorf("%d/%d unwatermarked inputs were attributed to a session", falsePositives, trials)
	}
}

// A sequence that was never handed out must not be attributed to whoever
// happens to sit closest to it.
func TestUnissuedSequenceDoesNotMatch(t *testing.T) {
	book := testBook(t)
	const unissued = 1234
	issued := issuedSet(40, 0) // deliberately excludes the payload in the leak
	soft := softFor(t, book, unissued, 0, book.ConfidentWindow(), 0.8, 0.2, 5)

	res, err := Attribute(book, soft, issued, 0)
	if err != nil {
		t.Fatalf("Attribute: %v", err)
	}
	if res.Matched {
		t.Errorf("an unissued sequence was attributed to %q at confidence %.3f",
			res.SessionID, res.Confidence)
	}
}

func TestAttributeRejectsShortWindows(t *testing.T) {
	book := testBook(t)
	soft := make(SoftSequence, book.MinWindow()-1)
	if _, err := Attribute(book, soft, issuedSet(4, 0), 0); err == nil {
		t.Fatal("Attribute with too few segments = nil error, want error")
	}
	if _, err := Attribute(nil, soft, nil, 0); err == nil {
		t.Fatal("Attribute with a nil codebook = nil error, want error")
	}
}

// The threshold is the control an operator has over their false-positive rate,
// so it must actually gate the answer.
func TestThresholdGatesTheAnswer(t *testing.T) {
	book := testBook(t)
	const payload = 9
	issued := issuedSet(40, payload)
	soft := softFor(t, book, payload, 0, book.ConfidentWindow(), 0.8, 0.2, 3)

	relaxed, err := Attribute(book, soft, issued, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	if !relaxed.Matched {
		t.Fatal("a clean leak should match at a relaxed threshold")
	}

	// Calibrate off the observed confidence so the test does not depend on how
	// clean this particular fixture happens to be.
	above := math.Nextafter(relaxed.Confidence, 2)
	strict, err := Attribute(book, soft, issued, above)
	if err != nil {
		t.Fatal(err)
	}
	if strict.Matched {
		t.Errorf("confidence %.3f matched against a threshold of %.6f", strict.Confidence, above)
	}
}

func TestResultReportsRecoveryDetail(t *testing.T) {
	book := testBook(t)
	const payload = 21
	issued := issuedSet(40, payload)
	soft := softFor(t, book, payload, 0, book.ConfidentWindow(), 0.8, 0.25, 11)

	res, err := Attribute(book, soft, issued, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.BitsTotal != book.ConfidentWindow() {
		t.Errorf("BitsTotal = %d, want %d", res.BitsTotal, book.ConfidentWindow())
	}
	if res.BitsRecovered > res.BitsTotal || res.BitsRecovered < 0 {
		t.Errorf("BitsRecovered %d is outside 0..%d", res.BitsRecovered, res.BitsTotal)
	}
}

func TestRankedSurfacesTheRunnersUp(t *testing.T) {
	book := testBook(t)
	const payload = 5
	issued := issuedSet(40, payload)
	soft := softFor(t, book, payload, 0, book.ConfidentWindow(), 0.8, 0.2, 13)

	ranked, err := Ranked(book, soft, issued, 5)
	if err != nil {
		t.Fatalf("Ranked: %v", err)
	}
	if len(ranked) != 5 {
		t.Fatalf("Ranked returned %d results, want 5", len(ranked))
	}
	if ranked[0].SessionID != "ses_target" {
		t.Errorf("top candidate is %q, want ses_target", ranked[0].SessionID)
	}
	for i := 1; i < len(ranked); i++ {
		if ranked[i].Confidence > ranked[i-1].Confidence {
			t.Fatal("results are not sorted by confidence")
		}
	}
	// The gap to the runner-up is what tells an investigator the winner stood
	// out rather than edging out a crowd.
	if ranked[0].Confidence-ranked[1].Confidence < 0.2 {
		t.Errorf("winner %.3f barely beat the runner-up %.3f", ranked[0].Confidence, ranked[1].Confidence)
	}
}

// Two sessions averaging their copies is the documented v1 limitation. This
// records it rather than pretending it works.
func TestCollusionIsNotResisted(t *testing.T) {
	book := testBook(t)
	const a, b = 3, 200
	issued := []Issued{{SessionID: "ses_a", PayloadID: a}, {SessionID: "ses_b", PayloadID: b}}

	seqA, _ := book.Sequence(a)
	seqB, _ := book.Sequence(b)
	window := book.ConfidentWindow()
	soft := make(SoftSequence, window)
	for i := range soft {
		// Where they agree the mark survives; where they differ it averages out,
		// which is exactly what a colluding pair produces.
		va, vb := -0.8, -0.8
		if seqA[i] == 1 {
			va = 0.8
		}
		if seqB[i] == 1 {
			vb = 0.8
		}
		soft[i] = (va + vb) / 2
	}

	res, err := Attribute(book, soft, issued, 0)
	if err != nil {
		t.Logf("collusion defeated decoding entirely: %v", err)
		return
	}
	if res.Matched {
		t.Logf("collusion still implicated %s at %.3f", res.SessionID, res.Confidence)
	} else {
		t.Logf("collusion produced no attribution (confidence %.3f, null %.3f)",
			res.Confidence, res.NullPeak)
	}
	t.Log("v1 is not collusion resistant; this test documents the limitation rather than asserting a fix")
}

func TestBookForRequiresAWatermarkedAsset(t *testing.T) {
	if _, err := BookFor(storage.Meta{Watermarked: false}); err == nil {
		t.Error("BookFor on an unwatermarked asset = nil error, want error")
	}
	if _, err := BookFor(storage.Meta{Watermarked: true}); err == nil {
		t.Error("BookFor without codebook params = nil error, want error")
	}
	params := codebook.Params{Version: codebook.Version, M: 5, T: 3, SegmentCount: 300}
	book, err := BookFor(storage.Meta{Watermarked: true, Codebook: &params})
	if err != nil {
		t.Fatalf("BookFor: %v", err)
	}
	if book.CodewordLen() != 31 {
		t.Errorf("rebuilt codebook has length %d, want 31", book.CodewordLen())
	}
}
