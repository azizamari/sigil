// Package server exposes the sigil HTTP API.
//
// sigil is not in the media path: it emits a few kilobytes of playlist per
// session and gets out of the way. Segments are fetched straight from the
// bucket or CDN using pre-signed URLs baked into that playlist.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/azizamari/sigil/internal/codebook"
	"github.com/azizamari/sigil/internal/manifest"
	"github.com/azizamari/sigil/internal/session"
	"github.com/azizamari/sigil/internal/storage"
)

const (
	playlistContentType = "application/vnd.apple.mpegurl"
	overlayDataID       = "dev.sigil.overlay"
)

type Config struct {
	APIKey string
	// BaseURL is where viewers reach this server, used to build playlist_url.
	BaseURL string
	// SegmentTTL must outlast the longest plausible viewing session or playback
	// breaks part way through. A scraped playlist stays usable for this window,
	// which is why the playlist token TTL is kept short instead.
	SegmentTTL     time.Duration
	AllowedOrigins []string
}

func (c Config) validate() error {
	switch {
	case c.APIKey == "":
		return errors.New("server: api key is required")
	case c.BaseURL == "":
		return errors.New("server: base url is required")
	case c.SegmentTTL <= 0:
		return errors.New("server: segment ttl must be positive")
	}
	return nil
}

type Server struct {
	cfg     Config
	storage *storage.Client
	minter  *session.Minter
	store   session.Store
	builder *manifest.Builder
	events  EventSink
	log     *slog.Logger
}

func New(cfg Config, store *storage.Client, minter *session.Minter, sessions session.Store, builder *manifest.Builder, events EventSink, log *slog.Logger) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if store == nil || minter == nil || sessions == nil || builder == nil {
		return nil, errors.New("server: storage, minter, session store and builder are required")
	}
	if log == nil {
		log = slog.Default()
	}
	if events == nil {
		events = NopSink{}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Server{cfg: cfg, storage: store, minter: minter, store: sessions, builder: builder, events: events, log: log}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/assets", s.authed(s.handleCreateAsset))
	mux.HandleFunc("GET /v1/assets/{id}", s.authed(s.handleGetAsset))
	mux.HandleFunc("POST /v1/sessions", s.authed(s.handleCreateSession))
	mux.HandleFunc("GET /v1/playlist/{session_id}", s.handlePlaylist)
	mux.HandleFunc("POST /v1/events", s.handleEvent)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return s.cors(mux)
}

// authed guards the server-to-server endpoints. Session creation in particular
// must never be reachable from a browser: viewer identity comes from the
// integrator's backend.
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.APIKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid api key")
			return
		}
		next(w, r)
	}
}

func (s *Server) cors(next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(s.cfg.AllowedOrigins))
	for _, o := range s.cfg.AllowedOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never reflect an arbitrary origin: the playlist is a credential.
		if origin := r.Header.Get("Origin"); origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Range")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type createAssetRequest struct {
	AssetID         string  `json:"asset_id"`
	SegmentCount    int     `json:"segment_count"`
	SegmentDuration float64 `json:"segment_duration_seconds"`
	TotalDuration   float64 `json:"total_duration_seconds"`
}

func (s *Server) handleCreateAsset(w http.ResponseWriter, r *http.Request) {
	var req createAssetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := storage.ValidateAssetID(req.AssetID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TotalDuration == 0 {
		req.TotalDuration = req.SegmentDuration * float64(req.SegmentCount)
	}

	meta := storage.Meta{
		Version:         storage.MetaVersion,
		AssetID:         req.AssetID,
		SegmentCount:    req.SegmentCount,
		SegmentDuration: req.SegmentDuration,
		TotalDuration:   req.TotalDuration,
		CreatedAt:       time.Now().UTC(),
	}
	if err := meta.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.storage.PutMeta(r.Context(), meta); err != nil {
		s.log.Error("write meta", "asset_id", req.AssetID, "error", err)
		writeError(w, http.StatusBadGateway, "could not write asset metadata")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"asset_id": meta.AssetID, "status": "ready"})
}

func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	meta, ok := s.loadMeta(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"asset_id":    meta.AssetID,
		"status":      "ready",
		"duration":    meta.TotalDuration,
		"segments":    meta.SegmentCount,
		"watermarked": meta.Watermarked,
	})
}

type createSessionRequest struct {
	AssetID     string `json:"asset_id"`
	OverlayText string `json:"overlay_text"`
	TTLSeconds  int    `json:"ttl"`
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	meta, ok := s.loadMeta(w, r, req.AssetID)
	if !ok {
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if req.TTLSeconds == 0 {
		ttl = time.Hour
	}

	var payloadID uint64
	if meta.Watermarked {
		book, err := codebook.New(*meta.Codebook)
		if err != nil {
			s.log.Error("build codebook", "asset_id", meta.AssetID, "error", err)
			writeError(w, http.StatusInternalServerError, "asset codebook is unusable")
			return
		}
		counter, err := s.store.Allocate(r.Context(), meta.AssetID, book.Capacity())
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		payloadID = session.PayloadIDFromSeed(counter, book.PayloadBits())
	}

	sess, token, err := s.minter.Mint(session.MintRequest{
		AssetID:     meta.AssetID,
		PayloadID:   payloadID,
		OverlayText: req.OverlayText,
		TTL:         ttl,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.Record(r.Context(), sess); err != nil {
		s.log.Error("record session", "session_id", sess.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not record session")
		return
	}
	// Deliberately no overlay text in this log line.
	s.log.Info("session issued", "session_id", sess.ID, "asset_id", sess.AssetID, "payload_id", sess.PayloadID)

	writeJSON(w, http.StatusCreated, map[string]any{
		"session_id":   sess.ID,
		"playlist_url": fmt.Sprintf("%s/v1/playlist/%s?t=%s", s.cfg.BaseURL, sess.ID, token),
		"expires_at":   sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	sess, overlay, err := s.minter.Open(r.PathValue("session_id"), r.URL.Query().Get("t"))
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, session.ErrExpired) {
			status = http.StatusGone
		}
		writeError(w, status, err.Error())
		return
	}
	meta, ok := s.loadMeta(w, r, sess.AssetID)
	if !ok {
		return
	}

	var sequence []uint8
	if meta.Watermarked {
		book, err := codebook.New(*meta.Codebook)
		if err != nil {
			s.log.Error("build codebook", "asset_id", meta.AssetID, "error", err)
			writeError(w, http.StatusInternalServerError, "asset codebook is unusable")
			return
		}
		if sequence, err = book.Sequence(sess.PayloadID); err != nil {
			s.log.Error("assign sequence", "session_id", sess.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "could not assign a sequence")
			return
		}
	}

	opts := manifest.Options{SegmentTTL: s.cfg.SegmentTTL}
	if overlay != "" {
		opts.SessionData = map[string]string{overlayDataID: overlay}
	}
	playlist, err := s.builder.Build(r.Context(), meta, sequence, opts)
	if err != nil {
		s.log.Error("build playlist", "session_id", sess.ID, "error", err)
		writeError(w, http.StatusBadGateway, "could not build playlist")
		return
	}

	w.Header().Set("Content-Type", playlistContentType)
	// Playlists are per session and must never be cached by a CDN or browser.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(playlist))
}

type eventRequest struct {
	SessionID string  `json:"session_id"`
	Token     string  `json:"token"`
	Type      string  `json:"type"`
	Position  float64 `json:"position"`
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	var req eventRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// Events arrive from the browser, so the session token authenticates them
	// rather than the API key.
	sess, _, err := s.minter.Open(req.SessionID, req.Token)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid session token")
		return
	}
	if !validEventType(req.Type) {
		writeError(w, http.StatusBadRequest, "unknown event type")
		return
	}

	ev := Event{
		SessionID: sess.ID,
		AssetID:   sess.AssetID,
		Type:      req.Type,
		Position:  req.Position,
		At:        time.Now().UTC(),
	}
	if err := s.events.Emit(r.Context(), ev); err != nil {
		s.log.Warn("emit event", "session_id", sess.ID, "type", req.Type, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func validEventType(t string) bool {
	switch t {
	case "start", "seek", "heartbeat", "complete":
		return true
	}
	return false
}

func (s *Server) loadMeta(w http.ResponseWriter, r *http.Request, assetID string) (storage.Meta, bool) {
	if err := storage.ValidateAssetID(assetID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return storage.Meta{}, false
	}
	meta, err := s.storage.GetMeta(r.Context(), assetID)
	if err != nil {
		s.log.Warn("load meta", "asset_id", assetID, "error", err)
		writeError(w, http.StatusNotFound, "asset not found")
		return storage.Meta{}, false
	}
	return meta, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
