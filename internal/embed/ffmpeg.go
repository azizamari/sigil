package embed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// FFmpeg is the pixel-domain embedder from SPEC 5 option A: a low-amplitude
// luma perturbation over a fixed block grid, applied as a filtergraph so no
// encoder patching is needed.
type FFmpeg struct {
	Binary  string
	Probe   string
	Encode  []string
	MaxSeek int
}

func NewFFmpeg() *FFmpeg {
	return &FFmpeg{
		Binary: "ffmpeg",
		Probe:  "ffprobe",
		Encode: []string{"-c:v", "libx264", "-preset", "medium", "-crf", "21", "-pix_fmt", "yuv420p"},
	}
}

var _ Embedder = (*FFmpeg)(nil)

func (f *FFmpeg) bin() string {
	if f.Binary == "" {
		return "ffmpeg"
	}
	return f.Binary
}

func (f *FFmpeg) probe() string {
	if f.Probe == "" {
		return "ffprobe"
	}
	return f.Probe
}

func (f *FFmpeg) Embed(ctx context.Context, src, dst string, variant uint8, p Params) error {
	pat, err := NewPattern(p)
	if err != nil {
		return err
	}
	plane, err := pat.GrayPlane(p, variant)
	if err != nil {
		return err
	}

	planePath, cleanup, err := writeTemp(plane)
	if err != nil {
		return err
	}
	defer cleanup()

	// Blending happens on an isolated luma plane so both blend inputs are gray:
	// no YUV range conversion can rescale the amplitude, and chroma is untouched.
	filter := "[0:v]format=yuv420p,extractplanes=y+u+v[y][u][v];" +
		"[1:v]loop=loop=-1:size=1:start=0,setpts=N/(30*TB)[wm];" +
		"[y][wm]blend=all_mode=grainmerge:shortest=1[ym];" +
		"[ym][u][v]mergeplanes=0x001020:yuv420p[out]"

	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", src,
		"-f", "rawvideo", "-pixel_format", "gray",
		"-video_size", fmt.Sprintf("%dx%d", p.Width, p.Height),
		"-framerate", "30", "-i", planePath,
		"-filter_complex", filter, "-map", "[out]", "-an",
	}
	args = append(args, f.Encode...)
	args = append(args, dst)

	cmd := exec.CommandContext(ctx, f.bin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("embed: ffmpeg failed for %s: %w: %s", src, err, stderr.String())
	}
	return nil
}

// Extract decodes at the file's own size rather than rescaling to the packaged
// grid: resampling to a guessed alignment is the thing the search has to solve.
func (f *FFmpeg) Extract(ctx context.Context, src string, p Params) (float64, error) {
	pat, err := NewPattern(p)
	if err != nil {
		return 0, err
	}
	frames, err := f.Frames(ctx, src, 8)
	if err != nil {
		return 0, err
	}
	if len(frames) == 0 {
		return 0, fmt.Errorf("embed: no frames decoded from %s", src)
	}
	opts := DefaultSearch()
	_, score := SearchAlignment(frames, pat, p, opts)
	null, err := NullEstimate(frames, p, opts)
	if err != nil {
		return 0, err
	}
	return SoftDecision(score, null), nil
}

func (f *FFmpeg) Frames(ctx context.Context, src string, limit int) ([]*Integral, error) {
	w, h, err := f.Size(ctx, src)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, f.bin(), "-hide_banner", "-loglevel", "error",
		"-i", src, "-pix_fmt", "gray", "-f", "rawvideo", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("embed: start decode of %s: %w", src, err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	var out []*Integral
	buf := make([]byte, w*h)
	for limit <= 0 || len(out) < limit {
		if _, err := io.ReadFull(stdout, buf); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return nil, fmt.Errorf("embed: read frame from %s: %w: %s", src, err, stderr.String())
		}
		in, err := NewIntegral(buf, w, h)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, nil
}

func (f *FFmpeg) Size(ctx context.Context, src string) (int, int, error) {
	out, err := exec.CommandContext(ctx, f.probe(), "-v", "error",
		"-select_streams", "v:0", "-show_entries", "stream=width,height",
		"-of", "csv=p=0:s=x", src).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("embed: probe %s: %w", src, err)
	}
	// A transport stream can report the same video stream more than once, so
	// only the first line is meaningful.
	first := ""
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			first = line
			break
		}
	}
	parts := strings.Split(first, "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("embed: unexpected probe output %q for %s", out, src)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("embed: parse width for %s: %w", src, err)
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("embed: parse height for %s: %w", src, err)
	}
	return w, h, nil
}

func writeTemp(data []byte) (string, func(), error) {
	dir, err := os.MkdirTemp("", "sigil-embed-")
	if err != nil {
		return "", nil, fmt.Errorf("embed: temp dir: %w", err)
	}
	path := filepath.Join(dir, "pattern.gray")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("embed: write pattern: %w", err)
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

// EmbedHLS writes a watermarked variant already segmented for HLS.
//
// Both variants must split at identical boundaries or a player switching
// between them mid-stream would corrupt the sequence, so key frames are forced
// on the segment grid rather than left to scene detection.
func (f *FFmpeg) EmbedHLS(ctx context.Context, src, outDir string, variant uint8, p Params, segmentSeconds float64) error {
	pat, err := NewPattern(p)
	if err != nil {
		return err
	}
	plane, err := pat.GrayPlane(p, variant)
	if err != nil {
		return err
	}
	if segmentSeconds <= 0 {
		return errors.New("embed: segment duration must be positive")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("embed: create %s: %w", outDir, err)
	}

	planePath, cleanup, err := writeTemp(plane)
	if err != nil {
		return err
	}
	defer cleanup()

	filter := "[0:v]format=yuv420p,extractplanes=y+u+v[y][u][v];" +
		"[1:v]loop=loop=-1:size=1:start=0,setpts=N/(30*TB)[wm];" +
		"[y][wm]blend=all_mode=grainmerge:shortest=1[ym];" +
		"[ym][u][v]mergeplanes=0x001020:yuv420p[out]"

	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", src,
		"-f", "rawvideo", "-pixel_format", "gray",
		"-video_size", fmt.Sprintf("%dx%d", p.Width, p.Height),
		"-framerate", "30", "-i", planePath,
		"-filter_complex", filter, "-map", "[out]", "-an",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%g)", segmentSeconds),
		"-sc_threshold", "0",
	}
	args = append(args, f.Encode...)
	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.FormatFloat(segmentSeconds, 'f', -1, 64),
		"-hls_playlist_type", "vod",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", filepath.Join(outDir, "seg_%05d.ts"),
		filepath.Join(outDir, "index.m3u8"),
	)

	cmd := exec.CommandContext(ctx, f.bin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("embed: ffmpeg segmenting failed for %s: %w: %s", src, err, stderr.String())
	}
	return nil
}

// Duration reports the source length in seconds.
func (f *FFmpeg) Duration(ctx context.Context, src string) (float64, error) {
	out, err := exec.CommandContext(ctx, f.probe(), "-v", "error",
		"-show_entries", "format=duration", "-of", "default=nw=1:nk=1", src).Output()
	if err != nil {
		return 0, fmt.Errorf("embed: probe duration %s: %w", src, err)
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("embed: parse duration for %s: %w", src, err)
	}
	return d, nil
}
