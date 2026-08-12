package detect

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/azizamari/sigil/internal/embed"
	"github.com/azizamari/sigil/internal/storage"
)

// FFmpegSplitter cuts a leaked file on the packaged segment grid.
type FFmpegSplitter struct {
	Binary string
}

func (s FFmpegSplitter) bin() string {
	if s.Binary == "" {
		return "ffmpeg"
	}
	return s.Binary
}

// Split re-segments from startOffset.
//
// It always re-encodes rather than stream-copying. A leaked file carries
// whatever key frames its re-encoder chose, which is almost never the packaged
// segment grid, and a copy-based split silently returns a handful of huge
// chunks instead of failing. Paying for one more encode generation is the cost
// of cutting where the watermark actually changes.
func (s FFmpegSplitter) Split(ctx context.Context, src string, segmentSeconds, startOffset float64, outDir string) ([]string, error) {
	if segmentSeconds <= 0 {
		return nil, fmt.Errorf("detect: segment duration must be positive")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("detect: create %s: %w", outDir, err)
	}

	args := []string{"-y", "-hide_banner", "-loglevel", "error"}
	if startOffset > 0 {
		args = append(args, "-ss", fmt.Sprintf("%g", startOffset))
	}
	args = append(args,
		"-i", src, "-an",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "16",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%g)", segmentSeconds),
		"-sc_threshold", "0",
		"-f", "segment",
		"-segment_time", fmt.Sprintf("%g", segmentSeconds),
		"-reset_timestamps", "1",
		filepath.Join(outDir, "part_%05d.ts"),
	)

	cmd := exec.CommandContext(ctx, s.bin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("detect: split %s: %w: %s", src, err, stderr.String())
	}
	return listParts(outDir)
}

func listParts(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "part_*.ts"))
	if err != nil {
		return nil, fmt.Errorf("detect: list parts in %s: %w", dir, err)
	}
	sort.Strings(matches)
	return matches, nil
}

// Run attributes a leaked file.
//
// The leak carries no segment boundaries, so the file is re-cut at several
// sub-segment offsets and the best-scoring attempt wins. Getting this wrong
// costs signal rather than correctness: a misaligned cut mixes two segments'
// marks and the confidence falls, which is the behaviour that should follow.
func (d *Detector) Run(ctx context.Context, leaked string, meta storage.Meta, issued []Issued) (Result, error) {
	if d.Analyzer == nil && d.Extractor == nil {
		return Result{}, fmt.Errorf("detect: no analyzer or extractor configured")
	}
	book, err := BookFor(meta)
	if err != nil {
		return Result{}, err
	}
	if meta.Embed == nil {
		return Result{}, fmt.Errorf("detect: asset has no embed parameters")
	}

	work, err := os.MkdirTemp("", "sigil-detect-")
	if err != nil {
		return Result{}, fmt.Errorf("detect: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	splitter := d.Splitter
	if splitter == nil {
		splitter = FFmpegSplitter{}
	}

	var best Result
	var bestErr error
	found := false
	for i, frac := range []float64{0, 1.0 / 3, 2.0 / 3} {
		offset := frac * meta.SegmentDuration
		dir := filepath.Join(work, fmt.Sprintf("offset%d", i))

		parts, err := splitter.Split(ctx, leaked, meta.SegmentDuration, offset, dir)
		if err != nil {
			bestErr = err
			continue
		}
		if len(parts) < book.MinWindow() {
			bestErr = fmt.Errorf("detect: leak yielded %d segments, need at least %d",
				len(parts), book.MinWindow())
			continue
		}

		soft, err := d.softFor(ctx, parts, *meta.Embed)
		if err != nil {
			bestErr = err
			continue
		}

		res, err := Attribute(book, soft, issued, d.Threshold)
		if err != nil {
			bestErr = err
			continue
		}
		if !found || res.Confidence > best.Confidence {
			best, found = res, true
		}
	}
	if !found {
		if bestErr != nil {
			return Result{}, bestErr
		}
		return Result{}, ErrNoMatch
	}
	return best, nil
}

var _ Splitter = FFmpegSplitter{}

// softFor prefers the frame-level analyzer, falling back to the per-segment
// extractor when only that is wired up.
func (d *Detector) softFor(ctx context.Context, parts []string, p embed.Params) (SoftSequence, error) {
	if d.Analyzer != nil {
		return softSequenceFor(ctx, d.Analyzer, parts, p)
	}
	soft := make(SoftSequence, 0, len(parts))
	for _, part := range parts {
		v, err := d.Extractor.Extract(ctx, part, p)
		if err != nil {
			return nil, err
		}
		soft = append(soft, v)
	}
	return soft, nil
}
