package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/azizamari/sigil/internal/codebook"
	"github.com/azizamari/sigil/internal/detect"
	"github.com/azizamari/sigil/internal/embed"
	"github.com/azizamari/sigil/internal/eval"
	"github.com/azizamari/sigil/internal/storage"
)

// runEval measures attribution accuracy. It is a benchmark, not a test: it
// reports rates over trials with fixed seeds and compares them to a committed
// baseline rather than asserting that any single detection succeeded.
func runEval(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var (
		out       = fs.String("out", "testdata/attacks/baseline.json", "where to write the measured report")
		baseline  = fs.String("baseline", "", "compare against this baseline and fail on regression")
		margin    = fs.Float64("margin", 0.10, "allowed movement before a cell counts as a regression")
		trials    = fs.Int("trials", 3, "sessions measured per cell")
		seconds   = fs.Int("seconds", 24, "fixture duration")
		segDur    = fs.Float64("segment-duration", 0.5, "segment length")
		width     = fs.Int("width", 960, "fixture width")
		height    = fs.Int("height", 540, "fixture height")
		fps       = fs.Int("fps", 15, "fixture frame rate")
		amplitude = fs.Int("amplitude", 0, "embed amplitude; 0 uses the shipped default")
		threshold = fs.Float64("threshold", detect.DefaultThreshold, "confidence a match must reach")
		only      = fs.String("fixtures", "", "comma-separated fixture subset for a fast smoke run")
		markdown  = fs.Bool("markdown", false, "print the table instead of the JSON path")
		workDir   = fs.String("work", "", "keep intermediate media here instead of a temp dir")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	work := *workDir
	if work == "" {
		var err error
		work, err = os.MkdirTemp("", "sigil-eval-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(work) }()
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}

	fixtures := eval.Fixtures(*width, *height, *fps, *seconds)
	if *only != "" {
		wanted := map[string]bool{}
		for _, name := range strings.Split(*only, ",") {
			wanted[strings.TrimSpace(name)] = true
		}
		var kept []eval.Fixture
		for _, f := range fixtures {
			if wanted[f.Name] {
				kept = append(kept, f)
			}
		}
		fixtures = kept
	}

	report := eval.Report{Threshold: *threshold}
	start := time.Now()
	for _, fixture := range fixtures {
		fmt.Fprintf(stdout, "== %s ==\n", fixture.Name)
		rates, err := measureFixture(ctx, stdout, fixture, work, evalConfig{
			seconds: *seconds, segDur: *segDur, width: *width, height: *height,
			amplitude: *amplitude, trials: *trials, threshold: *threshold,
		})
		if err != nil {
			return err
		}
		report.Rates = append(report.Rates, rates...)
	}
	fmt.Fprintf(stdout, "\nmeasured %d cells in %s\n", len(report.Rates), time.Since(start).Round(time.Second))

	if err := report.Save(*out); err != nil {
		return err
	}
	if *markdown {
		fmt.Fprint(stdout, "\n"+report.Markdown())
	} else {
		fmt.Fprintf(stdout, "wrote %s\n", *out)
	}

	if *baseline != "" {
		base, err := eval.LoadReport(*baseline)
		if err != nil {
			return err
		}
		regressions := eval.CompareToBaseline(report, base, *margin)
		for _, r := range regressions {
			fmt.Fprintln(stdout, "REGRESSION:", r)
		}
		if len(regressions) > 0 {
			return fmt.Errorf("eval: %d cells regressed beyond the %.2f margin", len(regressions), *margin)
		}
		fmt.Fprintln(stdout, "no regressions against the baseline")
	}
	return nil
}

type evalConfig struct {
	seconds, width, height, amplitude, trials int
	segDur, threshold                         float64
}

func measureFixture(ctx context.Context, stdout io.Writer, fixture eval.Fixture, work string, cfg evalConfig) ([]eval.Rate, error) {
	dir := filepath.Join(work, fixture.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	src, err := fixture.Render(ctx, dir, cfg.segDur)
	if err != nil {
		return nil, err
	}

	ep := embed.DefaultParams(cfg.width, cfg.height)
	if cfg.amplitude > 0 {
		ep.Amplitude = cfg.amplitude
	}
	embedder := embed.NewFFmpeg()
	embedder.Encode = []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-pix_fmt", "yuv420p"}

	variantDirs := map[uint8]string{}
	segmentCount := 0
	for _, variant := range []uint8{0, 1} {
		vdir := filepath.Join(dir, fmt.Sprintf("v%d", variant))
		if err := embedder.EmbedHLS(ctx, src, vdir, variant, ep, cfg.segDur); err != nil {
			return nil, err
		}
		segs, err := filepath.Glob(filepath.Join(vdir, "seg_*.ts"))
		if err != nil {
			return nil, err
		}
		variantDirs[variant] = vdir
		segmentCount = len(segs)
	}

	params, err := codebook.Fit(4, segmentCount, segmentCount)
	if err != nil {
		return nil, fmt.Errorf("eval: %w (fixture yielded %d segments)", err, segmentCount)
	}
	params.SegmentCount = segmentCount
	book, err := codebook.New(params)
	if err != nil {
		return nil, err
	}
	meta := storage.Meta{
		Version: storage.MetaVersion, AssetID: "eval", SegmentCount: segmentCount,
		SegmentDuration: cfg.segDur, TotalDuration: float64(cfg.seconds),
		Watermarked: true, Codebook: &params, Embed: &ep,
	}

	issued := make([]detect.Issued, 0, cfg.trials)
	for i := range cfg.trials {
		issued = append(issued, detect.Issued{
			SessionID: fmt.Sprintf("ses_%02d", i),
			PayloadID: uint64(i),
		})
	}
	detector := &detect.Detector{Analyzer: embedder, Threshold: cfg.threshold}

	sourceBitrate := cfg.width * cfg.height * 3
	clip := cfg.seconds
	var rates []eval.Rate

	for _, attack := range eval.AttackGrid() {
		rate := eval.Rate{
			Fixture: fixture.Name, Attack: attack.Name, ClipLen: clip,
			Trials: cfg.trials, Collusion: attack.Collusion,
		}
		var hits, errs int
		var confSum, nullSum float64
		var firstErr error

		for _, iss := range issued {
			leak, err := buildLeak(ctx, book, variantDirs, dir, iss.PayloadID, segmentCount, attack, cfg)
			if err != nil {
				return nil, err
			}
			degraded, err := eval.Apply(ctx, attack, leak, dir, sourceBitrate)
			if err != nil {
				return nil, err
			}
			res, err := detector.Run(ctx, degraded, meta, issued)
			if err != nil {
				// A failure to detect is a miss, but silently counting it as one
				// would hide a broken harness behind a plausible-looking rate.
				errs++
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			confSum += res.Confidence
			nullSum += res.NullPeak
			if res.Matched && res.SessionID == iss.SessionID {
				hits++
			}
		}
		rate.TPR = float64(hits) / float64(cfg.trials)
		rate.MeanConfidence = confSum / float64(cfg.trials)
		rate.MeanNull = nullSum / float64(cfg.trials)

		// The direction people skip: unwatermarked content must not be
		// attributed to anyone, whatever the attack did to it.
		fp, err := measureFalsePositive(ctx, detector, meta, issued, src, dir, attack, sourceBitrate)
		if err != nil {
			return nil, err
		}
		rate.FPR = fp

		rates = append(rates, rate)
		note := ""
		if errs > 0 {
			note = fmt.Sprintf("  [%d/%d failed: %v]", errs, cfg.trials, firstErr)
		}
		fmt.Fprintf(stdout, "  %-20s tpr %.2f  fpr %.2f  conf %.3f  null %.3f%s\n",
			attack.Name, rate.TPR, rate.FPR, rate.MeanConfidence, rate.MeanNull, note)
	}
	return rates, nil
}

// buildLeak assembles the file a session would walk away with. The collusion
// row interleaves two sessions' segments, which is what splicing produces.
func buildLeak(ctx context.Context, book *codebook.Codebook, variantDirs map[uint8]string, dir string, payload uint64, count int, attack eval.Attack, cfg evalConfig) (string, error) {
	seq, err := book.Sequence(payload)
	if err != nil {
		return "", err
	}
	var other []uint8
	if attack.Collusion {
		if other, err = book.Sequence(payload + 1); err != nil {
			return "", err
		}
	}

	var list strings.Builder
	for i := range count {
		variant := seq[i]
		if attack.Collusion && i%2 == 1 {
			variant = other[i]
		}
		fmt.Fprintf(&list, "file '%s'\n", filepath.Join(variantDirs[variant], fmt.Sprintf("seg_%05d.ts", i)))
	}
	listPath := filepath.Join(dir, "concat.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0o600); err != nil {
		return "", err
	}
	leak := filepath.Join(dir, "leak.mp4")
	return leak, eval.Concat(ctx, listPath, leak)
}

func measureFalsePositive(ctx context.Context, d *detect.Detector, meta storage.Meta, issued []detect.Issued, src, dir string, attack eval.Attack, bitrate int) (float64, error) {
	degraded, err := eval.Apply(ctx, attack, src, filepath.Join(dir), bitrate)
	if err != nil {
		return 0, err
	}
	res, err := d.Run(ctx, degraded, meta, issued)
	if err != nil {
		return 0, nil
	}
	if res.Matched {
		return 1, nil
	}
	return 0, nil
}
