// Package pack turns a source file into the two segment variants an asset
// needs, and records the parameters a detector will require later.
package pack

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/azizamari/sigil/internal/codebook"
	"github.com/azizamari/sigil/internal/embed"
	"github.com/azizamari/sigil/internal/storage"
)

type Options struct {
	AssetID         string
	SegmentDuration float64
	// DetectWindow is the shortest leaked clip attribution must work from. It
	// decides the codebook, so it is a packaging parameter, not a detector one.
	DetectWindow time.Duration
	Sessions     uint64
	Amplitude    int
	Block        int
	Seed         int64
	WorkDir      string
}

func (o *Options) applyDefaults() {
	if o.SegmentDuration == 0 {
		// 1s segments give exactly 90 in a 90s clip, three short of the 93 a
		// 14-bit payload needs. Segment duration is a detection parameter here,
		// not just a delivery one.
		o.SegmentDuration = 0.75
	}
	if o.DetectWindow == 0 {
		o.DetectWindow = 90 * time.Second
	}
	if o.Sessions == 0 {
		o.Sessions = 10000
	}
}

type Packer struct {
	Embedder *embed.FFmpeg
	Storage  *storage.Client
	Log      *slog.Logger
}

type Result struct {
	Meta     storage.Meta
	Uploaded int
}

// Plan works out the codebook and embed parameters without touching the source,
// so a caller can be told a configuration is impossible before spending an hour
// encoding it.
func Plan(width, height int, duration float64, o Options) (storage.Meta, embed.Params, error) {
	o.applyDefaults()
	if err := storage.ValidateAssetID(o.AssetID); err != nil {
		return storage.Meta{}, embed.Params{}, err
	}
	if duration <= 0 {
		return storage.Meta{}, embed.Params{}, fmt.Errorf("pack: source duration %.2f is not usable", duration)
	}

	segmentCount := int(math.Ceil(duration / o.SegmentDuration))
	windowSegments := int(o.DetectWindow.Seconds() / o.SegmentDuration)
	payloadBits := bitsFor(o.Sessions)

	cb, err := codebook.Fit(payloadBits, windowSegments, segmentCount)
	if err != nil {
		// Two different constraints reach here, and saying which one bit saves
		// the caller guessing: the clip may be too short, or the whole asset.
		return storage.Meta{}, embed.Params{}, fmt.Errorf(
			"pack: %w; at %.2fs per segment a %.0fs detection window holds %d segments and the asset has %d in total",
			err, o.SegmentDuration, o.DetectWindow.Seconds(), windowSegments, segmentCount)
	}
	cb.Seed = o.Seed

	ep := embed.DefaultParams(width, height)
	if o.Block > 0 {
		ep.Block = o.Block
	}
	if o.Amplitude > 0 {
		ep.Amplitude = o.Amplitude
	}
	ep.Seed = o.Seed
	if err := ep.Validate(); err != nil {
		return storage.Meta{}, embed.Params{}, err
	}

	meta := storage.Meta{
		Version:         storage.MetaVersion,
		AssetID:         o.AssetID,
		SegmentCount:    segmentCount,
		SegmentDuration: o.SegmentDuration,
		TotalDuration:   duration,
		Watermarked:     true,
		Codebook:        &cb,
		Embed:           &ep,
		CreatedAt:       time.Now().UTC(),
	}
	return meta, ep, meta.Validate()
}

func bitsFor(sessions uint64) int {
	bits := 1
	for uint64(1)<<uint(bits) < sessions && bits < 63 {
		bits++
	}
	return bits
}

// Run encodes both variants and uploads them. It is a batch job, never a
// request path.
func (p *Packer) Run(ctx context.Context, src string, o Options) (Result, error) {
	o.applyDefaults()
	if p.Embedder == nil || p.Storage == nil {
		return Result{}, fmt.Errorf("pack: embedder and storage are required")
	}
	log := p.Log
	if log == nil {
		log = slog.Default()
	}

	width, height, err := p.Embedder.Size(ctx, src)
	if err != nil {
		return Result{}, err
	}
	duration, err := p.Embedder.Duration(ctx, src)
	if err != nil {
		return Result{}, err
	}
	meta, ep, err := Plan(width, height, duration, o)
	if err != nil {
		return Result{}, err
	}

	work := o.WorkDir
	if work == "" {
		work, err = os.MkdirTemp("", "sigil-pack-")
		if err != nil {
			return Result{}, fmt.Errorf("pack: temp dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(work) }()
	}

	counts := map[uint8]int{}
	for _, variant := range []uint8{0, 1} {
		dir := filepath.Join(work, fmt.Sprintf("v%d", variant))
		log.Info("encoding variant", "asset_id", o.AssetID, "variant", variant)
		if err := p.Embedder.EmbedHLS(ctx, src, dir, variant, ep, o.SegmentDuration); err != nil {
			return Result{}, err
		}
		segments, err := listSegments(dir)
		if err != nil {
			return Result{}, err
		}
		counts[variant] = len(segments)
	}

	// A session's sequence is indexed by segment number, so a mismatch between
	// the two variant sets would silently misattribute every leak.
	if counts[0] != counts[1] {
		return Result{}, fmt.Errorf("pack: variants produced %d and %d segments; they must match",
			counts[0], counts[1])
	}
	meta.SegmentCount = counts[0]
	if meta.Codebook.SegmentCount = counts[0]; meta.SegmentCount < meta.Codebook.SegmentCount {
		return Result{}, fmt.Errorf("pack: segment count %d is inconsistent", meta.SegmentCount)
	}
	if err := meta.Validate(); err != nil {
		return Result{}, err
	}

	uploaded := 0
	for _, variant := range []uint8{0, 1} {
		dir := filepath.Join(work, fmt.Sprintf("v%d", variant))
		segments, err := listSegments(dir)
		if err != nil {
			return Result{}, err
		}
		for i, seg := range segments {
			key, err := storage.SegmentKey(o.AssetID, variant, i+1)
			if err != nil {
				return Result{}, err
			}
			f, err := os.Open(seg)
			if err != nil {
				return Result{}, fmt.Errorf("pack: open %s: %w", seg, err)
			}
			err = p.Storage.Put(ctx, key, f, "video/mp2t")
			_ = f.Close()
			if err != nil {
				return Result{}, err
			}
			uploaded++
		}
	}

	if err := p.Storage.PutMeta(ctx, meta); err != nil {
		return Result{}, err
	}
	log.Info("packaged asset", "asset_id", o.AssetID, "segments", meta.SegmentCount, "uploaded", uploaded)
	return Result{Meta: meta, Uploaded: uploaded}, nil
}

func listSegments(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "seg_*.ts"))
	if err != nil {
		return nil, fmt.Errorf("pack: list segments in %s: %w", dir, err)
	}
	sort.Strings(matches)
	return matches, nil
}
