package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func testMinter(t *testing.T) *Minter {
	t.Helper()
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMinter(key)
	if err != nil {
		t.Fatalf("NewMinter: %v", err)
	}
	return m
}

func TestNewMinterRejectsShortKey(t *testing.T) {
	if _, err := NewMinter(make([]byte, 16)); err == nil {
		t.Fatal("NewMinter with a 16-byte key = nil error, want error")
	}
}

func TestMintAndOpenRoundTrip(t *testing.T) {
	m := testMinter(t)
	const overlay = "viewer@example.com · order-42"
	s, token, err := m.Mint(MintRequest{
		AssetID: "lecture-01", PayloadID: 7, OverlayText: overlay, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !strings.HasPrefix(s.ID, IDPrefix) {
		t.Errorf("session id %q lacks the %q prefix", s.ID, IDPrefix)
	}

	got, gotOverlay, err := m.Open(s.ID, token)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.AssetID != "lecture-01" || got.PayloadID != 7 {
		t.Errorf("Open returned asset %q payload %d, want lecture-01 / 7", got.AssetID, got.PayloadID)
	}
	if gotOverlay != overlay {
		t.Errorf("Open returned overlay %q, want %q", gotOverlay, overlay)
	}
}

// The whole point of sealing is that the credential is opaque; if the overlay
// string were readable it would sit in plaintext in access logs.
func TestTokenDoesNotLeakOverlayText(t *testing.T) {
	m := testMinter(t)
	const overlay = "secret-viewer@example.com"
	_, token, err := m.Mint(MintRequest{AssetID: "a", OverlayText: overlay, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token, "secret-viewer") || strings.Contains(token, "example.com") {
		t.Errorf("token exposes overlay text: %s", token)
	}
}

func TestTokensAreUnique(t *testing.T) {
	m := testMinter(t)
	seen := make(map[string]bool)
	for range 100 {
		s, token, err := m.Mint(MintRequest{AssetID: "a", OverlayText: "same", TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if seen[s.ID] {
			t.Fatalf("session id %q issued twice", s.ID)
		}
		if seen[token] {
			t.Fatal("identical tokens issued for identical input")
		}
		seen[s.ID], seen[token] = true, true
	}
}

func TestOpenRejectsTamperedToken(t *testing.T) {
	m := testMinter(t)
	s, token, err := m.Mint(MintRequest{AssetID: "lecture-01", PayloadID: 3, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	flip := []byte(token)
	flip[len(flip)-1] ^= 'x'
	tests := []struct {
		name  string
		sid   string
		token string
	}{
		{"flipped byte", s.ID, string(flip)},
		{"truncated", s.ID, token[:len(token)-6]},
		{"empty", s.ID, ""},
		{"not base64", s.ID, "!!!!not-a-token!!!!"},
		{"token replayed under another session id", "ses_someoneelse", token},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := m.Open(tc.sid, tc.token); err == nil {
				t.Fatal("Open = nil error, want error")
			}
		})
	}
}

func TestOpenRejectsExpiredToken(t *testing.T) {
	m := testMinter(t)
	base := time.Now()
	m.now = func() time.Time { return base }

	s, token, err := m.Mint(MintRequest{AssetID: "a", TTL: MinTTL})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(s.ID, token); err != nil {
		t.Fatalf("Open before expiry: %v", err)
	}

	m.now = func() time.Time { return base.Add(MinTTL) }
	if _, _, err := m.Open(s.ID, token); !errors.Is(err, ErrExpired) {
		t.Errorf("Open at the expiry boundary = %v, want ErrExpired", err)
	}
	m.now = func() time.Time { return base.Add(MinTTL + time.Second) }
	if _, _, err := m.Open(s.ID, token); !errors.Is(err, ErrExpired) {
		t.Errorf("Open after expiry = %v, want ErrExpired", err)
	}
}

func TestMintValidates(t *testing.T) {
	m := testMinter(t)
	tests := []struct {
		name string
		req  MintRequest
	}{
		{"no asset", MintRequest{TTL: time.Hour}},
		{"ttl too short", MintRequest{AssetID: "a", TTL: time.Second}},
		{"ttl too long", MintRequest{AssetID: "a", TTL: MaxTTL + time.Minute}},
		{"overlay too large", MintRequest{AssetID: "a", TTL: time.Hour, OverlayText: strings.Repeat("x", MaxOverlayBytes+1)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := m.Mint(tc.req); err == nil {
				t.Fatal("Mint = nil error, want error")
			}
		})
	}
}

func TestTokenFromAnotherKeyIsRejected(t *testing.T) {
	a, b := testMinter(t), testMinter(t)
	s, token, err := a.Mint(MintRequest{AssetID: "lecture-01", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Open(s.ID, token); !errors.Is(err, ErrInvalid) {
		t.Errorf("Open with the wrong key = %v, want ErrInvalid", err)
	}
}

func TestMemoryStoreRecordAndLookup(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	s := Session{ID: "ses_a", AssetID: "lecture-01", PayloadID: 1}
	if err := store.Record(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := store.Lookup(ctx, "ses_a")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.AssetID != "lecture-01" {
		t.Errorf("Lookup returned asset %q, want lecture-01", got.AssetID)
	}
	if _, err := store.Lookup(ctx, "ses_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Lookup of a missing session = %v, want ErrNotFound", err)
	}
	if err := store.Record(ctx, Session{}); err == nil {
		t.Error("Record without an id = nil error, want error")
	}
}

func TestMemoryStoreListsByAssetWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	for _, s := range []Session{
		{ID: "ses_a", AssetID: "one"},
		{ID: "ses_b", AssetID: "one"},
		{ID: "ses_c", AssetID: "two"},
	} {
		if err := store.Record(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Record(ctx, Session{ID: "ses_a", AssetID: "one", PayloadID: 9}); err != nil {
		t.Fatal(err)
	}

	one, err := store.ListByAsset(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 2 {
		t.Errorf("asset one has %d sessions, want 2 (re-recording must not duplicate)", len(one))
	}
}

// Two sessions sharing a payload would carry identical sequences, making them
// indistinguishable in a leak.
func TestAllocateIsUniqueUntilCapacityRunsOut(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	seen := make(map[uint64]bool)
	for range 8 {
		id, err := store.Allocate(ctx, "lecture-01", 8)
		if err != nil {
			t.Fatalf("Allocate: %v", err)
		}
		if seen[id] {
			t.Fatalf("payload %d allocated twice", id)
		}
		seen[id] = true
	}
	if _, err := store.Allocate(ctx, "lecture-01", 8); err == nil {
		t.Error("Allocate past capacity = nil error, want error")
	}
	if _, err := store.Allocate(ctx, "other-asset", 8); err != nil {
		t.Errorf("capacity must be per asset: %v", err)
	}
}

func TestPayloadIDFitsCapacity(t *testing.T) {
	tests := []struct {
		counter uint64
		bits    int
		want    uint64
	}{
		{counter: 0, bits: 16, want: 0},
		{counter: 5, bits: 16, want: 5},
		{counter: 65536, bits: 16, want: 0},
		{counter: 65537, bits: 16, want: 1},
		{counter: 42, bits: 64, want: 42},
	}
	for _, tc := range tests {
		if got := PayloadIDFromSeed(tc.counter, tc.bits); got != tc.want {
			t.Errorf("PayloadIDFromSeed(%d, %d) = %d, want %d", tc.counter, tc.bits, got, tc.want)
		}
	}
}
