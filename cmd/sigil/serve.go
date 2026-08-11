package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/azizamari/sigil/internal/manifest"
	"github.com/azizamari/sigil/internal/server"
	"github.com/azizamari/sigil/internal/session"
	"github.com/azizamari/sigil/internal/signer"
	"github.com/azizamari/sigil/internal/storage"
)

func runServe(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var (
		addr        = fs.String("addr", ":8080", "listen address")
		baseURL     = fs.String("base-url", "", "public base URL of this server")
		bucket      = fs.String("s3-bucket", "", "bucket holding assets")
		endpoint    = fs.String("s3-endpoint", "", "custom S3 endpoint for MinIO, R2, B2")
		region      = fs.String("s3-region", "", "S3 region; R2 uses \"auto\"")
		pathStyle   = fs.Bool("s3-path-style", false, "path-style addressing, required by MinIO")
		signerName  = fs.String("signer", "s3-presign", "URL signer to use")
		segmentTTL  = fs.Duration("segment-ttl", 4*time.Hour, "expiry for segment URLs; must outlast a viewing session")
		origins     = fs.String("allowed-origins", "", "comma-separated browser origins allowed to fetch playlists")
		webhook     = fs.String("event-webhook", "", "URL to POST session events to")
		showKeyHelp = fs.Bool("print-key", false, "print a fresh session key and exit")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showKeyHelp {
		key, err := session.NewKey()
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(key))
		return nil
	}

	// Secrets come from the environment, never from flags, so they do not land
	// in shell history or a process list.
	apiKey := os.Getenv("SIGIL_API_KEY")
	if apiKey == "" {
		return errors.New("SIGIL_API_KEY is not set")
	}
	sessionKey, err := loadSessionKey()
	if err != nil {
		return err
	}
	if *baseURL == "" {
		return errors.New("--base-url is required")
	}
	if *signerName != "s3-presign" {
		return fmt.Errorf("unknown signer %q; only s3-presign is implemented", *signerName)
	}

	store, err := storage.New(ctx, storage.Config{
		Bucket: *bucket, Endpoint: *endpoint, Region: *region, PathStyle: *pathStyle,
	})
	if err != nil {
		return err
	}
	urlSigner, err := signer.NewS3(store.API(), store.Bucket())
	if err != nil {
		return err
	}
	builder, err := manifest.NewBuilder(urlSigner)
	if err != nil {
		return err
	}
	minter, err := session.NewMinter(sessionKey)
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	var sink server.EventSink = server.LogSink{Log: log}
	if *webhook != "" {
		sink = server.WebhookSink{URL: *webhook}
	}

	srv, err := server.New(server.Config{
		APIKey:         apiKey,
		BaseURL:        *baseURL,
		SegmentTTL:     *segmentTTL,
		AllowedOrigins: splitAndTrim(*origins),
	}, store, minter, session.NewMemoryStore(), builder, sink, log)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("sigil serve listening", "addr", *addr, "bucket", store.Bucket())
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
}

// loadSessionKey reads the key that seals playlist tokens. Sessions minted
// under a previous key stop resolving when it changes, which is the intended
// way to revoke every outstanding playlist at once.
func loadSessionKey() ([]byte, error) {
	raw := os.Getenv("SIGIL_SESSION_KEY")
	if raw == "" {
		return nil, errors.New("SIGIL_SESSION_KEY is not set; generate one with: sigil serve --print-key")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("SIGIL_SESSION_KEY is not valid base64: %w", err)
	}
	if len(key) != session.KeySize {
		return nil, fmt.Errorf("SIGIL_SESSION_KEY must decode to %d bytes, got %d", session.KeySize, len(key))
	}
	return key, nil
}

func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
