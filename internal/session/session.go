// Package session mints and validates viewing sessions.
//
// Overlay text is never stored. It is sealed into the playlist token with
// authenticated encryption, so it survives only in the credential the caller
// holds: sigil keeps no copy, and the string never appears in a URL, an access
// log or a referrer header in readable form.
package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	IDPrefix = "ses_"
	// KeySize is the sealing key length. AES-256-GCM binds every field of the
	// token, so a tampered asset id or expiry fails to open rather than
	// silently granting access.
	KeySize = 32

	MaxOverlayBytes = 512
	MinTTL          = time.Minute
	MaxTTL          = 24 * time.Hour
)

var (
	ErrExpired  = errors.New("session: token has expired")
	ErrMismatch = errors.New("session: token does not match the requested session")
	ErrInvalid  = errors.New("session: token is invalid")
)

type Session struct {
	ID        string    `json:"session_id"`
	AssetID   string    `json:"asset_id"`
	PayloadID uint64    `json:"payload_id"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// claims are sealed; field names stay short because the token rides in a URL.
type claims struct {
	SID     string `json:"s"`
	AID     string `json:"a"`
	PID     uint64 `json:"p"`
	Exp     int64  `json:"e"`
	Overlay string `json:"o,omitempty"`
}

type Minter struct {
	aead cipher.AEAD
	now  func() time.Time
}

func NewMinter(key []byte) (*Minter, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("session: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("session: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("session: new gcm: %w", err)
	}
	return &Minter{aead: aead, now: time.Now}, nil
}

func NewKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("session: generate key: %w", err)
	}
	return key, nil
}

type MintRequest struct {
	AssetID     string
	PayloadID   uint64
	OverlayText string
	TTL         time.Duration
}

func (m *Minter) Mint(req MintRequest) (Session, string, error) {
	switch {
	case req.AssetID == "":
		return Session{}, "", errors.New("session: asset id is required")
	case len(req.OverlayText) > MaxOverlayBytes:
		return Session{}, "", fmt.Errorf("session: overlay text is %d bytes, limit is %d",
			len(req.OverlayText), MaxOverlayBytes)
	case req.TTL < MinTTL || req.TTL > MaxTTL:
		return Session{}, "", fmt.Errorf("session: ttl must be between %s and %s, got %s",
			MinTTL, MaxTTL, req.TTL)
	}

	id, err := newID()
	if err != nil {
		return Session{}, "", err
	}
	now := m.now()
	s := Session{
		ID:        id,
		AssetID:   req.AssetID,
		PayloadID: req.PayloadID,
		IssuedAt:  now,
		ExpiresAt: now.Add(req.TTL),
	}
	token, err := m.seal(claims{
		SID:     s.ID,
		AID:     s.AssetID,
		PID:     s.PayloadID,
		Exp:     s.ExpiresAt.Unix(),
		Overlay: req.OverlayText,
	})
	if err != nil {
		return Session{}, "", err
	}
	return s, token, nil
}

// Open returns the session and its overlay text. sessionID comes from the URL
// path and must match the sealed claim, so a token cannot be replayed under a
// different session's identity.
func (m *Minter) Open(sessionID, token string) (Session, string, error) {
	c, err := m.open(token)
	if err != nil {
		return Session{}, "", err
	}
	if c.SID != sessionID {
		return Session{}, "", ErrMismatch
	}
	expires := time.Unix(c.Exp, 0)
	if !m.now().Before(expires) {
		return Session{}, "", ErrExpired
	}
	return Session{
		ID:        c.SID,
		AssetID:   c.AID,
		PayloadID: c.PID,
		ExpiresAt: expires,
	}, c.Overlay, nil
}

func (m *Minter) seal(c claims) (string, error) {
	plain, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("session: encode claims: %w", err)
	}
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("session: nonce: %w", err)
	}
	sealed := m.aead.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (m *Minter) open(token string) (claims, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return claims{}, ErrInvalid
	}
	if len(raw) < m.aead.NonceSize() {
		return claims{}, ErrInvalid
	}
	nonce, ct := raw[:m.aead.NonceSize()], raw[m.aead.NonceSize():]
	plain, err := m.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return claims{}, ErrInvalid
	}
	var c claims
	if err := json.Unmarshal(plain, &c); err != nil {
		return claims{}, ErrInvalid
	}
	return c, nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("session: generate id: %w", err)
	}
	return IDPrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// PayloadIDFromSeed derives a codebook payload from a counter, keeping issued
// ids inside the code's capacity.
func PayloadIDFromSeed(counter uint64, payloadBits int) uint64 {
	if payloadBits >= 64 {
		return counter
	}
	return counter & (1<<uint(payloadBits) - 1)
}
