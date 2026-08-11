package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/azizamari/sigil/internal/codebook"
	"github.com/azizamari/sigil/internal/embed"
	"github.com/azizamari/sigil/internal/manifest"
	"github.com/azizamari/sigil/internal/session"
	"github.com/azizamari/sigil/internal/storage"
)

const testAPIKey = "test-api-key"

// fakeS3 serves just enough of the S3 API for meta.json round trips, keeping
// this layer at L1: no containers, no network beyond the loopback test server.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/videos/")
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		body, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type fixture struct {
	server  *Server
	handler http.Handler
	store   *session.MemoryStore
	s3      *fakeS3
	events  *captureSink
}

type captureSink struct {
	mu     sync.Mutex
	events []Event
}

func (c *captureSink) Emit(_ context.Context, e Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

func (c *captureSink) all() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.events...)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fake := &fakeS3{objects: map[string][]byte{}}
	s3srv := httptest.NewServer(fake)
	t.Cleanup(s3srv.Close)

	awsCfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""),
	}
	store := storage.NewFromAWS(awsCfg, storage.Config{
		Bucket: "videos", Endpoint: s3srv.URL, Region: "us-east-1", PathStyle: true,
	})

	key, err := session.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	minter, err := session.NewMinter(key)
	if err != nil {
		t.Fatal(err)
	}
	signer := &staticSigner{}
	builder, err := manifest.NewBuilder(signer)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewMemoryStore()
	events := &captureSink{}

	srv, err := New(Config{
		APIKey:         testAPIKey,
		BaseURL:        "https://sigil.example.com",
		SegmentTTL:     4 * time.Hour,
		AllowedOrigins: []string{"https://app.example.com"},
	}, store, minter, sessions, builder, events, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &fixture{server: srv, handler: srv.Handler(), store: sessions, s3: fake, events: events}
}

type staticSigner struct{}

func (staticSigner) Sign(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://cdn.example.com/" + key + "?sig=x", nil
}

func (f *fixture) do(t *testing.T, method, path string, body any, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	for _, o := range opts {
		o(req)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	return w
}

func noAuth(r *http.Request) { r.Header.Del("Authorization") }

func (f *fixture) createAsset(t *testing.T, id string, segments int) {
	t.Helper()
	resp := f.do(t, http.MethodPost, "/v1/assets", map[string]any{
		"asset_id":                 id,
		"segment_count":            segments,
		"segment_duration_seconds": 4.0,
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create asset returned %d: %s", resp.Code, resp.Body)
	}
}

// createWatermarkedAsset writes meta directly, since packaging lands later.
func (f *fixture) createWatermarkedAsset(t *testing.T, id string, segments int) storage.Meta {
	t.Helper()
	params := codebook.Params{Version: codebook.Version, M: 5, T: 3, SegmentCount: segments}
	ep := embed.DefaultParams(1280, 720)
	meta := storage.Meta{
		Version:         storage.MetaVersion,
		AssetID:         id,
		SegmentCount:    segments,
		SegmentDuration: 1,
		TotalDuration:   float64(segments),
		Watermarked:     true,
		Codebook:        &params,
		Embed:           &ep,
		CreatedAt:       time.Now().UTC(),
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	f.s3.mu.Lock()
	f.s3.objects[storage.MetaKey(id)] = raw
	f.s3.mu.Unlock()
	return meta
}

func (f *fixture) mintSession(t *testing.T, assetID, overlay string) (string, string) {
	t.Helper()
	resp := f.do(t, http.MethodPost, "/v1/sessions", map[string]any{
		"asset_id": assetID, "overlay_text": overlay, "ttl": 3600,
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create session returned %d: %s", resp.Code, resp.Body)
	}
	var out struct {
		SessionID   string `json:"session_id"`
		PlaylistURL string `json:"playlist_url"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(out.PlaylistURL)
	if err != nil {
		t.Fatal(err)
	}
	return out.SessionID, u.Query().Get("t")
}

func TestAuthIsRequiredOnServerToServerEndpoints(t *testing.T) {
	f := newFixture(t)
	paths := []struct{ method, path string }{
		{http.MethodPost, "/v1/assets"},
		{http.MethodGet, "/v1/assets/lecture-01"},
		{http.MethodPost, "/v1/sessions"},
	}
	for _, p := range paths {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			if got := f.do(t, p.method, p.path, map[string]any{}, noAuth); got.Code != http.StatusUnauthorized {
				t.Errorf("without an api key = %d, want 401", got.Code)
			}
		})
	}
	wrongKey := func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }
	if got := f.do(t, http.MethodPost, "/v1/sessions", map[string]any{}, wrongKey); got.Code != http.StatusUnauthorized {
		t.Errorf("with the wrong api key = %d, want 401", got.Code)
	}
}

func TestCreateAndGetAsset(t *testing.T) {
	f := newFixture(t)
	f.createAsset(t, "lecture-01", 10)

	resp := f.do(t, http.MethodGet, "/v1/assets/lecture-01", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("get asset returned %d: %s", resp.Code, resp.Body)
	}
	var out struct {
		AssetID  string  `json:"asset_id"`
		Duration float64 `json:"duration"`
		Segments int     `json:"segments"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.AssetID != "lecture-01" || out.Segments != 10 || out.Duration != 40 {
		t.Errorf("got %+v, want lecture-01 / 10 segments / 40s", out)
	}
}

func TestCreateAssetRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name string
		body map[string]any
	}{
		{"path traversal in id", map[string]any{"asset_id": "../etc", "segment_count": 1, "segment_duration_seconds": 4.0}},
		{"no segments", map[string]any{"asset_id": "a", "segment_count": 0, "segment_duration_seconds": 4.0}},
		{"zero duration", map[string]any{"asset_id": "a", "segment_count": 1, "segment_duration_seconds": 0.0}},
		{"unknown field", map[string]any{"asset_id": "a", "segment_count": 1, "segment_duration_seconds": 4.0, "surprise": 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.do(t, http.MethodPost, "/v1/assets", tc.body); got.Code != http.StatusBadRequest {
				t.Errorf("= %d, want 400 (%s)", got.Code, got.Body)
			}
		})
	}
}

func TestGetUnknownAssetIs404(t *testing.T) {
	f := newFixture(t)
	if got := f.do(t, http.MethodGet, "/v1/assets/nope", nil); got.Code != http.StatusNotFound {
		t.Errorf("= %d, want 404", got.Code)
	}
}

func TestPlaylistIsServedAndNotCacheable(t *testing.T) {
	f := newFixture(t)
	f.createAsset(t, "lecture-01", 5)
	sid, token := f.mintSession(t, "lecture-01", "viewer@example.com")

	resp := f.do(t, http.MethodGet, "/v1/playlist/"+sid+"?t="+url.QueryEscape(token), nil, noAuth)
	if resp.Code != http.StatusOK {
		t.Fatalf("playlist returned %d: %s", resp.Code, resp.Body)
	}
	if ct := resp.Header().Get("Content-Type"); ct != playlistContentType {
		t.Errorf("Content-Type = %q, want %q", ct, playlistContentType)
	}
	if cc := resp.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	body := resp.Body.String()
	if !strings.HasPrefix(body, "#EXTM3U") {
		t.Errorf("playlist does not start with #EXTM3U:\n%s", body)
	}
	if n := strings.Count(body, "#EXTINF:"); n != 5 {
		t.Errorf("playlist has %d segments, want 5", n)
	}
	want := manifest.OverlayTag + base64.StdEncoding.EncodeToString([]byte("viewer@example.com"))
	if !strings.Contains(body, want) {
		t.Errorf("playlist does not carry the overlay tag:\n%s", body)
	}
}

// The playlist URL is the credential, so an unauthenticated or altered one must
// not return a playable manifest.
func TestPlaylistRejectsBadCredentials(t *testing.T) {
	f := newFixture(t)
	f.createAsset(t, "lecture-01", 3)
	sid, token := f.mintSession(t, "lecture-01", "")
	otherSid, _ := f.mintSession(t, "lecture-01", "")

	tests := []struct {
		name string
		path string
		want int
	}{
		{"no token", "/v1/playlist/" + sid, http.StatusForbidden},
		{"empty token", "/v1/playlist/" + sid + "?t=", http.StatusForbidden},
		{"garbage token", "/v1/playlist/" + sid + "?t=not-a-token", http.StatusForbidden},
		{"token replayed under another session", "/v1/playlist/" + otherSid + "?t=" + url.QueryEscape(token), http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.do(t, http.MethodGet, tc.path, nil, noAuth); got.Code != tc.want {
				t.Errorf("= %d, want %d", got.Code, tc.want)
			}
		})
	}
}

func TestExpiredPlaylistTokenIsGone(t *testing.T) {
	f := newFixture(t)
	f.createAsset(t, "lecture-01", 3)
	sid, token := f.mintSession(t, "lecture-01", "")

	f.server.minter.SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
	resp := f.do(t, http.MethodGet, "/v1/playlist/"+sid+"?t="+url.QueryEscape(token), nil, noAuth)
	if resp.Code != http.StatusGone {
		t.Errorf("expired playlist = %d, want 410", resp.Code)
	}
}

// Each session must receive the variant sequence its payload maps to, or
// attribution points at the wrong viewer.
func TestPlaylistFollowsTheAssignedSequence(t *testing.T) {
	f := newFixture(t)
	meta := f.createWatermarkedAsset(t, "lecture-01", 93)
	sid, token := f.mintSession(t, "lecture-01", "")

	sess, err := f.store.Lookup(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	book, err := codebook.New(*meta.Codebook)
	if err != nil {
		t.Fatal(err)
	}
	want, err := book.Sequence(sess.PayloadID)
	if err != nil {
		t.Fatal(err)
	}

	resp := f.do(t, http.MethodGet, "/v1/playlist/"+sid+"?t="+url.QueryEscape(token), nil, noAuth)
	if resp.Code != http.StatusOK {
		t.Fatalf("playlist returned %d: %s", resp.Code, resp.Body)
	}
	for i, bit := range want {
		if !strings.Contains(resp.Body.String(), fmt.Sprintf("/v%d/seg_%05d.ts", bit, i+1)) {
			t.Fatalf("segment %d does not use variant %d", i+1, bit)
		}
	}
}

func TestSessionsGetDistinctPayloads(t *testing.T) {
	f := newFixture(t)
	f.createWatermarkedAsset(t, "lecture-01", 93)
	seen := map[uint64]bool{}
	for range 5 {
		sid, _ := f.mintSession(t, "lecture-01", "")
		sess, err := f.store.Lookup(context.Background(), sid)
		if err != nil {
			t.Fatal(err)
		}
		if seen[sess.PayloadID] {
			t.Fatalf("payload %d issued to two sessions", sess.PayloadID)
		}
		seen[sess.PayloadID] = true
	}
}

func TestCreateSessionValidates(t *testing.T) {
	f := newFixture(t)
	f.createAsset(t, "lecture-01", 3)
	tests := []struct {
		name string
		body map[string]any
		want int
	}{
		{"unknown asset", map[string]any{"asset_id": "nope", "ttl": 3600}, http.StatusNotFound},
		{"bad asset id", map[string]any{"asset_id": "../etc", "ttl": 3600}, http.StatusBadRequest},
		{"ttl too long", map[string]any{"asset_id": "lecture-01", "ttl": 90000}, http.StatusBadRequest},
		{"overlay too large", map[string]any{"asset_id": "lecture-01", "ttl": 3600, "overlay_text": strings.Repeat("x", 600)}, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.do(t, http.MethodPost, "/v1/sessions", tc.body); got.Code != tc.want {
				t.Errorf("= %d, want %d (%s)", got.Code, tc.want, got.Body)
			}
		})
	}
}

func TestEventsRequireASessionToken(t *testing.T) {
	f := newFixture(t)
	f.createAsset(t, "lecture-01", 3)
	sid, token := f.mintSession(t, "lecture-01", "")

	resp := f.do(t, http.MethodPost, "/v1/events", map[string]any{
		"session_id": sid, "token": token, "type": "start", "position": 0.0,
	}, noAuth)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("event returned %d: %s", resp.Code, resp.Body)
	}
	events := f.events.all()
	if len(events) != 1 || events[0].Type != "start" || events[0].SessionID != sid {
		t.Fatalf("captured events = %+v", events)
	}

	bad := f.do(t, http.MethodPost, "/v1/events", map[string]any{
		"session_id": sid, "token": "forged", "type": "start",
	}, noAuth)
	if bad.Code != http.StatusForbidden {
		t.Errorf("event with a forged token = %d, want 403", bad.Code)
	}

	unknown := f.do(t, http.MethodPost, "/v1/events", map[string]any{
		"session_id": sid, "token": token, "type": "exfiltrate",
	}, noAuth)
	if unknown.Code != http.StatusBadRequest {
		t.Errorf("unknown event type = %d, want 400", unknown.Code)
	}
}

func TestEventsCarryNoViewerIdentity(t *testing.T) {
	f := newFixture(t)
	f.createAsset(t, "lecture-01", 3)
	sid, token := f.mintSession(t, "lecture-01", "viewer@example.com")
	f.do(t, http.MethodPost, "/v1/events", map[string]any{
		"session_id": sid, "token": token, "type": "heartbeat", "position": 12.5,
	}, noAuth)

	raw, err := json.Marshal(f.events.all())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "viewer@example.com") {
		t.Errorf("emitted event contains overlay text: %s", raw)
	}
}

func TestCORSOnlyEchoesAllowedOrigins(t *testing.T) {
	f := newFixture(t)
	f.createAsset(t, "lecture-01", 3)
	sid, token := f.mintSession(t, "lecture-01", "")
	path := "/v1/playlist/" + sid + "?t=" + url.QueryEscape(token)

	allowed := f.do(t, http.MethodGet, path, nil, noAuth, func(r *http.Request) {
		r.Header.Set("Origin", "https://app.example.com")
	})
	if got := allowed.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("allowed origin header = %q, want the app origin", got)
	}
	if got := allowed.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Content-Range") {
		t.Errorf("Expose-Headers = %q, want it to include Content-Range for seeking", got)
	}

	denied := f.do(t, http.MethodGet, path, nil, noAuth, func(r *http.Request) {
		r.Header.Set("Origin", "https://attacker.example")
	})
	if got := denied.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unlisted origin was echoed as %q", got)
	}
}

func TestHealthz(t *testing.T) {
	f := newFixture(t)
	if got := f.do(t, http.MethodGet, "/healthz", nil, noAuth); got.Code != http.StatusOK {
		t.Errorf("healthz = %d, want 200", got.Code)
	}
}
