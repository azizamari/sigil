// Spike: can a hand-crafted A/B luma watermark survive a re-encode well enough
// to tell variant 0 from variant 1? Throwaway code, deliberately crude.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	flagDir       = flag.String("dir", "scratch/spike", "working directory for generated media")
	flagBlock     = flag.Int("block", 40, "watermark block edge in pixels")
	flagSeed      = flag.Int64("seed", 20260811, "pattern seed")
	flagSecs      = flag.Int("secs", 15, "fixture duration")
	flagCRF       = flag.String("crf", "21", "x264 CRF for source and packaging")
	flagAmps      = flag.String("amps", "2,4,6", "luma amplitudes to sweep")
	flagWidth     = flag.Int("width", 1280, "packaging and correlation width")
	flagHeight    = flag.Int("height", 720, "packaging and correlation height")
	flagFPS       = flag.Int("fps", 30, "fixture frame rate")
	flagKeepMedia = flag.Bool("keep", false, "keep generated media on exit")
	flagRung      = flag.Int("rung", 0, "package at this fixed kbps like an ABR rung; 0 uses CRF")
	flagMode      = flag.String("mode", "feasibility", "feasibility | align")
)

// A real ladder encodes each rung at a fixed bitrate whatever the content, so
// CRF packaging flatters flat material by letting it collapse to a few kbps.
func packageArgs() []string {
	if *flagRung > 0 {
		return x264(*flagRung)
	}
	return []string{"-c:v", "libx264", "-preset", "medium", "-crf", *flagCRF, "-pix_fmt", "yuv420p", "-an"}
}

type fixture struct {
	name  string
	lavfi string
}

func fixtures() []fixture {
	geom := fmt.Sprintf("%dx%d", frameW, frameH)
	return []fixture{
		{
			name:  "motion",
			lavfi: fmt.Sprintf("testsrc2=size=%s:rate=%d:duration=%d", geom, fps, *flagSecs),
		},
		{name: "screencast", lavfi: screencast()},
	}
}

// A screencast of purely static slides compresses to a few kbps, which makes a
// "60% of source bitrate" attack meaningless. Real capture has a cursor,
// scrolling and periodic redraws, so the fixture models those.
func screencast() string {
	canvasH := frameH + 900
	var b strings.Builder
	fmt.Fprintf(&b, "color=c=0xF2F2F2:s=%dx%d:r=%d:d=%d", frameW, canvasH, fps, *flagSecs)
	fmt.Fprintf(&b, ",drawbox=x=0:y=0:w=%d:h=64:color=0x2B3A55:t=fill", frameW)
	fmt.Fprintf(&b, ",drawbox=x=90:y=120:w=700:h=40:color=0x202020:t=fill")

	r := 1
	for i := 0; i < 26; i++ {
		r = (r*1103515245 + 12345) & 0x7fffffff
		y := 210 + i*52
		if y > canvasH-60 {
			break
		}
		switch i % 7 {
		case 3:
			fmt.Fprintf(&b, ",drawbox=x=120:y=%d:w=900:h=44:color=0xE4E9F0:t=fill", y)
			fmt.Fprintf(&b, ",drawbox=x=140:y=%d:w=%d:h=14:color=0x3A4A5A:t=fill", y+15, 300+r%420)
		default:
			fmt.Fprintf(&b, ",drawbox=x=120:y=%d:w=%d:h=14:color=0x505050:t=fill", y, 420+r%560)
		}
	}

	scroll := fmt.Sprintf("if(lt(t,%d),0,if(lt(t,%d),(t-%d)*180,%d))",
		*flagSecs/3, 2**flagSecs/3, *flagSecs/3, 180**flagSecs/3)
	fmt.Fprintf(&b, ",crop=%d:%d:0:'min(%d,%s)'", frameW, frameH, canvasH-frameH, scroll)
	fmt.Fprintf(&b, ",drawbox=x='%d+%d*abs(sin(t*0.9))':y='%d+%d*abs(cos(t*0.6))':w=11:h=17:color=0x101010:t=fill",
		frameW/6, frameW/2, frameH/5, frameH/2)
	return b.String()
}

type attack struct {
	name string
	args []string
}

func main() {
	flag.Parse()
	frameW, frameH, fps = *flagWidth, *flagHeight, *flagFPS
	runner := runSpike
	if *flagMode == "align" {
		runner = runAlign
	}
	if err := runner(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "spike:", err)
		os.Exit(1)
	}
}

func runSpike(ctx context.Context) error {
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
	fmt.Printf("grid %dx%d blocks of %dpx (%d blocks), seed %d, %ds fixtures at crf %s\n",
		p.NX, p.NY, *flagBlock, p.NX*p.NY, *flagSeed, *flagSecs, *flagCRF)

	for _, amp := range amps {
		for _, v := range []int8{1, -1} {
			if err := p.WriteGrayPlane(patternPath(dir, amp, v), amp, v); err != nil {
				return err
			}
		}
	}

	for _, f := range fixtures() {
		src := filepath.Join(dir, f.name+"_src.mp4")
		if err := run(ctx, append(append([]string{"-f", "lavfi", "-i", f.lavfi}, packageArgs()...), src)...); err != nil {
			return err
		}
		srcRate, err := probeBitrate(ctx, src)
		if err != nil {
			return err
		}
		fmt.Printf("\n=== %s — source %d kbps ===\n", f.name, srcRate/1000)
		for _, amp := range amps {
			if err := reportAmp(ctx, dir, f, src, srcRate, amp, p); err != nil {
				return err
			}
		}
	}
	return nil
}

func reportAmp(ctx context.Context, dir string, f fixture, src string, srcRate, amp int, p *Pattern) error {
	variants := map[int8]string{}
	for _, v := range []int8{1, -1} {
		out := filepath.Join(dir, fmt.Sprintf("%s_a%d_v%d.mp4", f.name, amp, variantIndex(v)))
		if err := embed(ctx, src, patternPath(dir, amp, v), out, packageArgs()); err != nil {
			return err
		}
		variants[v] = out
	}
	wmRate, err := probeBitrate(ctx, variants[1])
	if err != nil {
		return err
	}
	fmt.Printf("\namplitude %d — packaged %d kbps (%+.1f%% vs source)\n",
		amp, wmRate/1000, 100*float64(wmRate-srcRate)/float64(srcRate))
	fmt.Printf("  %-20s %9s %9s %9s %9s %7s %8s %8s\n",
		"attack", "v0", "v1", "null", "null sd", "sep", "1s acc", "3s acc")

	rateK := wmRate * 60 / 100 / 1000
	for _, a := range []attack{
		{"clean", nil},
		{"reencode 60%", x264(rateK)},
		{"480p+reenc 60%", append([]string{"-vf", "scale=854:480"}, x264(rateK)...)},
		// Crop shifts the block grid off the pixels it was embedded on, so this
		// row measures alignment sensitivity, not robustness (SPEC risk 13.1).
		{"crop 5%+reenc 60%", append([]string{"-vf", "crop=iw*0.9:ih*0.9"}, x264(rateK)...)},
	} {
		scores := map[int8][]float64{}
		for _, v := range []int8{1, -1} {
			path, err := applyAttack(ctx, variants[v], a)
			if err != nil {
				return err
			}
			if scores[v], err = scoreFile(ctx, path, p); err != nil {
				return err
			}
		}
		nullPath, err := applyAttack(ctx, src, a)
		if err != nil {
			return err
		}
		null, err := scoreFile(ctx, nullPath, p)
		if err != nil {
			return err
		}

		sep := (mean(scores[1]) - mean(scores[-1])) / (2 * stddev(null))
		fmt.Printf("  %-20s %+9.3f %+9.3f %+9.3f %9.3f %7.1f %7.0f%% %7.0f%%\n",
			a.name, mean(scores[1]), mean(scores[-1]), mean(null), stddev(null), sep,
			100*pairAccuracy(scores, fps), 100*pairAccuracy(scores, fps*3))
	}
	return nil
}

func applyAttack(ctx context.Context, in string, a attack) (string, error) {
	if a.args == nil {
		return in, nil
	}
	out := strings.TrimSuffix(in, ".mp4") + "_" + slug(a.name) + ".mp4"
	return out, run(ctx, append(append([]string{"-i", in}, a.args...), out)...)
}

// pairAccuracy scores both variants together: a detector that always answered
// "variant 0" would sit at 50%, which is the number to beat.
func pairAccuracy(scores map[int8][]float64, window int) float64 {
	return (windowAccuracy(scores[1], window, true) + windowAccuracy(scores[-1], window, false)) / 2
}

func scoreFile(ctx context.Context, path string, p *Pattern) ([]float64, error) {
	var scores []float64
	err := frames(ctx, path, func(buf []byte) error {
		scores = append(scores, score(buf, p))
		return nil
	})
	return scores, err
}

func patternPath(dir string, amp int, polarity int8) string {
	return filepath.Join(dir, fmt.Sprintf("pat_a%d_v%d.gray", amp, variantIndex(polarity)))
}

func variantIndex(polarity int8) int {
	if polarity == 1 {
		return 0
	}
	return 1
}

func slug(s string) string {
	return strings.NewReplacer(" ", "", "%", "", "+", "_").Replace(s)
}

func parseAmps(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return nil, fmt.Errorf("bad amplitude %q: %w", f, err)
		}
		out = append(out, v)
	}
	return out, nil
}

func probeBitrate(ctx context.Context, path string) (int, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-show_entries", "format=bit_rate", "-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		return 0, fmt.Errorf("probe %s: %w", path, err)
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}
