package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Event carries no viewer identity. The integrator resolves a session id
// through their own database.
type Event struct {
	SessionID string    `json:"session_id"`
	AssetID   string    `json:"asset_id"`
	Type      string    `json:"type"`
	Position  float64   `json:"position"`
	At        time.Time `json:"at"`
}

type EventSink interface {
	Emit(ctx context.Context, e Event) error
}

type NopSink struct{}

func (NopSink) Emit(context.Context, Event) error { return nil }

type LogSink struct{ Log *slog.Logger }

func (l LogSink) Emit(_ context.Context, e Event) error {
	log := l.Log
	if log == nil {
		log = slog.Default()
	}
	log.Info("session event", "session_id", e.SessionID, "asset_id", e.AssetID,
		"type", e.Type, "position", e.Position)
	return nil
}

type WebhookSink struct {
	URL    string
	Client *http.Client
}

func (h WebhookSink) Emit(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("server: encode event: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("server: build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("server: post webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("server: webhook returned %s", resp.Status)
	}
	return nil
}
