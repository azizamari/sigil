package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Spike 2: crop killed detection in spike 1 because the block grid no longer
// sits on the pixels it was embedded on. This searches for the alignment
// instead of assuming it, which is SPEC open question 1.

type alignment struct {
	crop   float64
	dx, dy int
}

type alignResult struct {
	alignment
	score float64
}

// integral is a summed-area table, so the mean of any block rectangle costs
// four lookups no matter how large the search space gets.
type integral struct {
	w, h int
	sum  []float64
}

func newIntegral(frame []byte, w, h int) *integral {
	in := &integral{w: w, h: h, sum: make([]float64, (w+1)*(h+1))}
	for y := range h {
		var row float64
		for x := range w {
			row += float64(frame[y*w+x])
			in.sum[(y+1)*(w+1)+x+1] = in.sum[y*(w+1)+x+1] + row
		}
	}
	return in
}

func (in *integral) mean(x0, y0, x1, y1 int) float64 {
	x0, y0 = clamp(x0, 0, in.w), clamp(y0, 0, in.h)
	x1, y1 = clamp(x1, 0, in.w), clamp(y1, 0, in.h)
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	s := in.sum[y1*(in.w+1)+x1] - in.sum[y0*(in.w+1)+x1] - in.sum[y1*(in.w+1)+x0] + in.sum[y0*(in.w+1)+x0]
	return s / float64((x1-x0)*(y1-y0))
}

// scoreAligned rebuilds the canonical block grid under a candidate crop and
// shift, then correlates it with the pattern exactly as the aligned detector
// would.
func scoreAligned(ints []*integral, p *Pattern, a alignment) float64 {
	if len(ints) == 0 {
		return 0
	}
	var total float64
	for _, in := range ints {
		// The leak shows the source region [c, 1-c] scaled to its own size.
		srcW := float64(p.W) * (1 - 2*a.crop)
		srcH := float64(p.H) * (1 - 2*a.crop)
		sx := float64(in.w) / srcW
		sy := float64(in.h) / srcH
		ox := -float64(p.W)*a.crop*sx + float64(a.dx)
		oy := -float64(p.H)*a.crop*sy + float64(a.dy)

		means := make([]float64, p.NX*p.NY)
		for by := range p.NY {
			for bx := range p.NX {
				x0 := int(float64(bx*p.Block)*sx + ox)
				y0 := int(float64(by*p.Block)*sy + oy)
				x1 := int(float64((bx+1)*p.Block)*sx + ox)
				y1 := int(float64((by+1)*p.Block)*sy + oy)
				means[by*p.NX+bx] = in.mean(x0, y0, x1, y1)
			}
		}
		total += correlateGrid(means, p)
	}
	return total / float64(len(ints))
}

func correlateGrid(means []float64, p *Pattern) float64 {
	var sum float64
	var n int
	for by := 1; by < p.NY-1; by++ {
		for bx := 1; bx < p.NX-1; bx++ {
			var around float64
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if dx != 0 || dy != 0 {
						around += means[(by+dy)*p.NX+bx+dx]
					}
				}
			}
			sum += (means[by*p.NX+bx] - around/8) * float64(p.At(bx, by))
			n++
		}
	}
	return sum / float64(n)
}

// searchAlignment maximises |score|: the sign identifies the variant, so the
// alignment peak is where the magnitude is largest either way.
func searchAlignment(ints []*integral, p *Pattern, coarse bool) alignResult {
	best := alignResult{score: 0}
	cropStep, shiftStep, shiftMax := 0.01, 4, 12
	if !coarse {
		cropStep, shiftStep, shiftMax = 0.0025, 1, 5
	}
	for crop := 0.0; crop <= 0.12+1e-9; crop += cropStep {
		for dy := -shiftMax; dy <= shiftMax; dy += shiftStep {
			for dx := -shiftMax; dx <= shiftMax; dx += shiftStep {
				a := alignment{crop: crop, dx: dx, dy: dy}
				s := scoreAligned(ints, p, a)
				if math.Abs(s) > math.Abs(best.score) {
					best = alignResult{alignment: a, score: s}
				}
			}
		}
	}
	return best
}

func refineAlignment(ints []*integral, p *Pattern, around alignment) alignResult {
	best := alignResult{alignment: around, score: scoreAligned(ints, p, around)}
	for crop := around.crop - 0.01; crop <= around.crop+0.01+1e-9; crop += 0.0025 {
		for dy := around.dy - 4; dy <= around.dy+4; dy++ {
			for dx := around.dx - 4; dx <= around.dx+4; dx++ {
				a := alignment{crop: math.Max(0, crop), dx: dx, dy: dy}
				s := scoreAligned(ints, p, a)
				if math.Abs(s) > math.Abs(best.score) {
					best = alignResult{alignment: a, score: s}
				}
			}
		}
	}
	return best
}

func probeSize(ctx context.Context, path string) (int, int, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-select_streams", "v:0", "-show_entries", "stream=width,height",
		"-of", "csv=p=0:s=x", path).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("probe size %s: %w", path, err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "x")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected ffprobe size output %q", out)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return w, h, nil
}

// nativeFrames decodes without rescaling: the search must see the leak at the
// size it arrived, since resampling to a guessed grid is the thing being solved.
func nativeFrames(ctx context.Context, path string, limit int) ([]*integral, error) {
	w, h, err := probeSize(ctx, path)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error",
		"-i", path, "-pix_fmt", "gray", "-f", "rawvideo", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	var out []*integral
	buf := make([]byte, w*h)
	for len(out) < limit {
		if _, err := readFull(stdout, buf); err != nil {
			break
		}
		out = append(out, newIntegral(buf, w, h))
	}
	return out, nil
}

func readFull(r io.Reader, buf []byte) (int, error) { return io.ReadFull(r, buf) }

// runAlign answers one question: after a crop that destroyed detection in
// spike 1, can searching for the alignment recover the variant, and is the peak
// distinguishable from unwatermarked content?
func runAlign(ctx context.Context) error {
	dir := *flagDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if !*flagKeepMedia {
		defer func() { _ = os.RemoveAll(dir) }()
	}
	amps, err := parseAmps(*flagAmps)
	if err != nil {
		return err
	}
	p := NewPattern(frameW, frameH, *flagBlock, *flagSeed)
	fmt.Printf("alignment search: grid %dx%d of %dpx, crop 0-12%%, shift +/-12px\n\n", p.NX, p.NY, *flagBlock)

	for _, amp := range amps {
		for _, v := range []int8{1, -1} {
			if err := p.WriteGrayPlane(patternPath(dir, amp, v), amp, v); err != nil {
				return err
			}
		}
	}

	f := fixtures()[0]
	src := filepath.Join(dir, f.name+"_src.mp4")
	if err := run(ctx, append(append([]string{"-f", "lavfi", "-i", f.lavfi}, packageArgs()...), src)...); err != nil {
		return err
	}
	srcRate, err := probeBitrate(ctx, src)
	if err != nil {
		return err
	}
	rateK := srcRate * 60 / 100 / 1000

	fmt.Printf("  %-6s %-18s %8s %8s %8s %7s %7s\n", "amp", "input", "crop", "dx", "dy", "score", "variant")
	for _, amp := range amps {
		for _, v := range []int8{1, -1} {
			marked := filepath.Join(dir, fmt.Sprintf("%s_a%d_v%d.mp4", f.name, amp, variantIndex(v)))
			if err := embed(ctx, src, patternPath(dir, amp, v), marked, packageArgs()); err != nil {
				return err
			}
			attacked := strings.TrimSuffix(marked, ".mp4") + "_crop.mp4"
			if err := run(ctx, append(append([]string{"-i", marked, "-vf", "crop=iw*0.9:ih*0.9"}, x264(rateK)...), attacked)...); err != nil {
				return err
			}
			if err := reportAlign(ctx, p, attacked, fmt.Sprintf("v%d cropped", variantIndex(v)), amp, v); err != nil {
				return err
			}
		}
		// The null: same attack, no watermark. Its peak is the score a real
		// match has to beat.
		null := filepath.Join(dir, f.name+"_null_crop.mp4")
		if err := run(ctx, append(append([]string{"-i", src, "-vf", "crop=iw*0.9:ih*0.9"}, x264(rateK)...), null)...); err != nil {
			return err
		}
		if err := reportAlign(ctx, p, null, "unwatermarked", amp, 0); err != nil {
			return err
		}
	}
	return nil
}

func reportAlign(ctx context.Context, p *Pattern, path, label string, amp int, want int8) error {
	ints, err := nativeFrames(ctx, path, 6)
	if err != nil {
		return err
	}
	if len(ints) == 0 {
		return fmt.Errorf("no frames decoded from %s", path)
	}
	best := refineAlignment(ints, p, searchAlignment(ints, p, true).alignment)

	variant := "none"
	if want != 0 {
		if (best.score > 0) == (want == 1) {
			variant = "correct"
		} else {
			variant = "WRONG"
		}
	}
	fmt.Printf("  %-6d %-18s %7.2f%% %8d %8d %+7.3f %7s\n",
		amp, label, best.crop*100, best.dx, best.dy, best.score, variant)
	return nil
}
