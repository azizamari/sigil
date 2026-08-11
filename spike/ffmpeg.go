package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// In the product these belong in per-asset EmbedParams persisted to meta.json,
// since a detector must rebuild the exact grid an asset was packaged with.
// The spike runs one configuration at a time, set once from flags in main.
var (
	frameW = 1280
	frameH = 720
	fps    = 30
)

func run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-y", "-hide_banner", "-loglevel", "error"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg %v: %w: %s", args, err, stderr.String())
	}
	return nil
}

func x264(bitrateK int) []string {
	return []string{
		"-c:v", "libx264", "-preset", "medium",
		"-b:v", fmt.Sprintf("%dk", bitrateK),
		"-maxrate", fmt.Sprintf("%dk", bitrateK),
		"-bufsize", fmt.Sprintf("%dk", bitrateK*2),
		"-pix_fmt", "yuv420p", "-an",
	}
}

// Blending happens on an isolated luma plane: both blend inputs are then gray,
// so no YUV range conversion can rescale the amplitude out from under us.
func embed(ctx context.Context, src, patternPath, dst string, encode []string) error {
	filter := "[0:v]format=yuv420p,extractplanes=y+u+v[y][u][v];" +
		fmt.Sprintf("[1:v]loop=loop=-1:size=1:start=0,setpts=N/(%d*TB)[wm];", fps) +
		"[y][wm]blend=all_mode=grainmerge:shortest=1[ym];" +
		"[ym][u][v]mergeplanes=0x001020:yuv420p[out]"

	args := []string{
		"-i", src,
		"-f", "rawvideo", "-pixel_format", "gray",
		"-video_size", fmt.Sprintf("%dx%d", frameW, frameH),
		"-framerate", fmt.Sprint(fps), "-i", patternPath,
		"-filter_complex", filter, "-map", "[out]",
	}
	return run(ctx, append(append(args, encode...), dst)...)
}

// frames streams decoded luma, normalised back to the packaging resolution the
// way a real detector would have to before correlating.
func frames(ctx context.Context, path string, fn func([]byte) error) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path,
		"-vf", fmt.Sprintf("scale=%d:%d:flags=bicubic", frameW, frameH),
		"-pix_fmt", "gray", "-f", "rawvideo", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	buf := make([]byte, frameW*frameH)
	var loopErr error
	for {
		if _, err := io.ReadFull(stdout, buf); err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				loopErr = err
			}
			break
		}
		if err := fn(buf); err != nil {
			loopErr = err
			break
		}
	}
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil && loopErr == nil {
		return fmt.Errorf("decode %s: %w: %s", path, err, stderr.String())
	}
	return loopErr
}
