package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var ErrNotFound = errors.New("session: not found")

// Store records issued sessions so a leak can later be matched against the
// sequences that were actually handed out. It holds no viewer identity: the
// integrator resolves a session id through their own database.
type Store interface {
	Record(ctx context.Context, s Session) error
	Lookup(ctx context.Context, id string) (Session, error)
	ListByAsset(ctx context.Context, assetID string) ([]Session, error)
	// Allocate reserves a codebook payload for an asset. Two sessions sharing a
	// payload would carry identical sequences and be indistinguishable.
	Allocate(ctx context.Context, assetID string, capacity uint64) (uint64, error)
}

type MemoryStore struct {
	mu       sync.RWMutex
	byID     map[string]Session
	byAsset  map[string][]string
	counters map[string]uint64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:     make(map[string]Session),
		byAsset:  make(map[string][]string),
		counters: make(map[string]uint64),
	}
}

func (m *MemoryStore) Record(_ context.Context, s Session) error {
	if s.ID == "" {
		return errors.New("session: cannot record a session without an id")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.byID[s.ID]; !exists {
		m.byAsset[s.AssetID] = append(m.byAsset[s.AssetID], s.ID)
	}
	m.byID[s.ID] = s
	return nil
}

func (m *MemoryStore) Lookup(_ context.Context, id string) (Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.byID[id]
	if !ok {
		return Session{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return s, nil
}

func (m *MemoryStore) ListByAsset(_ context.Context, assetID string) ([]Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := m.byAsset[assetID]
	out := make([]Session, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.byID[id])
	}
	return out, nil
}

func (m *MemoryStore) Allocate(_ context.Context, assetID string, capacity uint64) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.counters[assetID]
	if capacity > 0 && next >= capacity {
		return 0, fmt.Errorf("session: asset %q has issued all %d available sequences", assetID, capacity)
	}
	m.counters[assetID] = next + 1
	return next, nil
}
