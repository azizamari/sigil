// Package eval measures how well attribution survives real degradation.
//
// This is a benchmark, not a test suite. Nothing here asserts that detection
// succeeded; it produces rates over many trials with fixed seeds, and the
// thresholds live in a committed baseline.
package eval

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Attack degrades a leaked file the way a re-uploader would.
type Attack struct {
	Name string
	// Filters is the ffmpeg -vf chain; empty means no spatial change.
	Filters string
	// BitrateFraction re-encodes at this fraction of the source bitrate.
	// Zero keeps the source quality.
	BitrateFraction float64
	FrameRate       int
	// Collusion marks an attack v1 is documented not to survive. It is measured
	// and reported rather than quietly omitted.
	Collusion bool
}

// AttackGrid is the matrix from SPEC 10. The negative rows are not optional:
// without them the numbers only describe how often the detector fires, not how
// often it is right.
func AttackGrid() []Attack {
	return []Attack{
		{Name: "clean"},
		{Name: "reencode_80", BitrateFraction: 0.8},
		{Name: "reencode_60", BitrateFraction: 0.6},
		{Name: "reencode_40", BitrateFraction: 0.4},
		{Name: "scale_720", Filters: "scale=1280:720", BitrateFraction: 0.6},
		{Name: "scale_480", Filters: "scale=854:480", BitrateFraction: 0.6},
		{Name: "crop_2", Filters: "crop=iw*0.96:ih*0.96", BitrateFraction: 0.6},
		{Name: "crop_5", Filters: "crop=iw*0.90:ih*0.90", BitrateFraction: 0.6},
		{Name: "crop_10", Filters: "crop=iw*0.80:ih*0.80", BitrateFraction: 0.6},
		{Name: "framerate_24", FrameRate: 24, BitrateFraction: 0.6},
		{Name: "combined", Filters: "crop=iw*0.95:ih*0.95,scale=1280:720", BitrateFraction: 0.6},
		{Name: "collusion_average", Collusion: true},
	}
}

// Apply writes the degraded copy and returns its path.
func Apply(ctx context.Context, a Attack, src, outDir string, sourceBitrate int) (string, error) {
	dst := filepath.Join(outDir, a.Name+".mp4")
	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-i", src}

	var filters []string
	if a.Filters != "" {
		filters = append(filters, a.Filters)
	}
	if a.FrameRate > 0 {
		filters = append(filters, fmt.Sprintf("fps=%d", a.FrameRate))
	}
	if len(filters) > 0 {
		args = append(args, "-vf", strings.Join(filters, ","))
	}

	// Key frames stay on the segment grid; an attacker re-encoding would not
	// preserve them, but the detector re-cuts the file anyway and this keeps the
	// harness measuring watermark robustness rather than container quirks.
	args = append(args, "-an", "-c:v", "libx264", "-preset", "veryfast")
	if a.BitrateFraction > 0 && sourceBitrate > 0 {
		rate := int(float64(sourceBitrate) * a.BitrateFraction / 1000)
		if rate < 50 {
			rate = 50
		}
		args = append(args,
			"-b:v", fmt.Sprintf("%dk", rate),
			"-maxrate", fmt.Sprintf("%dk", rate),
			"-bufsize", fmt.Sprintf("%dk", rate*2))
	} else {
		args = append(args, "-crf", "20")
	}
	args = append(args, "-pix_fmt", "yuv420p", dst)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("eval: attack %s: %w: %s", a.Name, err, stderr.String())
	}
	return dst, nil
}

// Trim cuts a window out of a leak, which is how partial-content detection is
// measured: attribution has to work from a clip, not only the whole asset.
func Trim(ctx context.Context, src, dst string, start, seconds float64) error {
	args := []string{"-y", "-hide_banner", "-loglevel", "error"}
	if start > 0 {
		args = append(args, "-ss", fmt.Sprintf("%g", start))
	}
	args = append(args, "-i", src, "-t", fmt.Sprintf("%g", seconds), "-an", "-c:v", "copy", dst)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("eval: trim %s: %w: %s", src, err, stderr.String())
	}
	return nil
}

// Concat stitches segment files back into one video, which is how a leak is
// reconstructed from the segments a session was served.
func Concat(ctx context.Context, listPath, dst string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", dst)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("eval: concat %s: %w: %s", listPath, err, stderr.String())
	}
	return nil
}
