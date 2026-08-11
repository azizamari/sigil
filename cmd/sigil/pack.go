package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/azizamari/sigil/internal/embed"
	"github.com/azizamari/sigil/internal/pack"
	"github.com/azizamari/sigil/internal/storage"
)

func runPack(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var (
		assetID   = fs.String("asset-id", "", "asset identifier")
		segDur    = fs.Float64("segment-duration", 0.75, "segment length in seconds")
		window    = fs.Duration("detect-window", 90*time.Second, "shortest leaked clip attribution must work from")
		sessions  = fs.Uint64("sessions", 10000, "number of distinguishable sessions to support")
		amplitude = fs.Int("amplitude", 0, "luma amplitude; 0 uses the default")
		block     = fs.Int("block", 0, "watermark block size in pixels; 0 scales with the frame")
		seed      = fs.Int64("seed", 1, "pattern seed recorded in meta.json")
		dryRun    = fs.Bool("dry-run", false, "report the plan without encoding or uploading")
		bucket    = fs.String("s3-bucket", "", "bucket to upload into")
		endpoint  = fs.String("s3-endpoint", "", "custom S3 endpoint")
		region    = fs.String("s3-region", "", "S3 region")
		pathStyle = fs.Bool("s3-path-style", false, "path-style addressing, required by MinIO")
	)
	// flag stops at the first positional, so a leading source path is pulled out
	// before parsing rather than forcing flags to come first.
	var src string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		src, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if src == "" && fs.NArg() == 1 {
		src = fs.Arg(0)
	}
	if src == "" {
		return fmt.Errorf("usage: sigil pack <source> --asset-id <id>")
	}
	if *assetID == "" {
		return fmt.Errorf("--asset-id is required")
	}

	opts := pack.Options{
		AssetID:         *assetID,
		SegmentDuration: *segDur,
		DetectWindow:    *window,
		Sessions:        *sessions,
		Amplitude:       *amplitude,
		Block:           *block,
		Seed:            *seed,
	}
	embedder := embed.NewFFmpeg()

	width, height, err := embedder.Size(ctx, src)
	if err != nil {
		return err
	}
	duration, err := embedder.Duration(ctx, src)
	if err != nil {
		return err
	}
	meta, ep, err := pack.Plan(width, height, duration, opts)
	if err != nil {
		return err
	}

	nx, ny := ep.Blocks()
	fmt.Fprintf(stdout, "source      %dx%d, %.1fs\n", width, height, duration)
	fmt.Fprintf(stdout, "segments    %d at %.2fs\n", meta.SegmentCount, meta.SegmentDuration)
	fmt.Fprintf(stdout, "watermark   %dx%d blocks of %dpx, amplitude %d\n", nx, ny, ep.Block, ep.Amplitude)
	fmt.Fprintf(stdout, "codebook    BCH over GF(2^%d), corrects %d, %d sessions\n",
		meta.Codebook.M, meta.Codebook.T, *sessions)
	if *dryRun {
		return nil
	}

	store, err := storage.New(ctx, storage.Config{
		Bucket: *bucket, Endpoint: *endpoint, Region: *region, PathStyle: *pathStyle,
	})
	if err != nil {
		return err
	}
	packer := &pack.Packer{
		Embedder: embedder,
		Storage:  store,
		Log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
	result, err := packer.Run(ctx, src, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "uploaded    %d objects to %s\n", result.Uploaded, storage.AssetPrefix(*assetID))
	return nil
}
