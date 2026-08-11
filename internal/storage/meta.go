package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/azizamari/sigil/internal/codebook"
)

const MetaVersion = 1

// Meta describes a packaged asset. It is versioned because embed parameters and
// the codebook will change, and assets packaged under older settings must stay
// detectable years later.
type Meta struct {
	Version         int              `json:"version"`
	AssetID         string           `json:"asset_id"`
	SegmentCount    int              `json:"segment_count"`
	SegmentDuration float64          `json:"segment_duration_seconds"`
	TotalDuration   float64          `json:"total_duration_seconds"`
	Watermarked     bool             `json:"watermarked"`
	Codebook        *codebook.Params `json:"codebook,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
}

func (m Meta) Validate() error {
	switch {
	case m.Version != MetaVersion:
		return fmt.Errorf("storage: unsupported meta version %d, want %d", m.Version, MetaVersion)
	case m.SegmentCount < 1:
		return errors.New("storage: meta needs at least one segment")
	case m.SegmentDuration <= 0:
		return errors.New("storage: meta needs a positive segment duration")
	case m.Watermarked && m.Codebook == nil:
		return errors.New("storage: watermarked asset has no codebook parameters")
	}
	return ValidateAssetID(m.AssetID)
}

// TargetDuration is the EXT-X-TARGETDURATION value, which HLS requires to be an
// integer at least as large as any segment.
func (m Meta) TargetDuration() int {
	d := int(m.SegmentDuration)
	if float64(d) < m.SegmentDuration {
		d++
	}
	return d
}

func (c *Client) PutMeta(ctx context.Context, m Meta) error {
	if err := m.Validate(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("storage: encode meta: %w", err)
	}
	return c.Put(ctx, MetaKey(m.AssetID), newByteReader(body), "application/json")
}

func (c *Client) GetMeta(ctx context.Context, assetID string) (Meta, error) {
	if err := ValidateAssetID(assetID); err != nil {
		return Meta{}, err
	}
	body, err := c.Get(ctx, MetaKey(assetID))
	if err != nil {
		return Meta{}, err
	}
	defer body.Close()

	raw, err := io.ReadAll(body)
	if err != nil {
		return Meta{}, fmt.Errorf("storage: read meta for %q: %w", assetID, err)
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, fmt.Errorf("storage: decode meta for %q: %w", assetID, err)
	}
	if err := m.Validate(); err != nil {
		return Meta{}, err
	}
	return m, nil
}

// SegmentDurationAt reports the duration of one segment. The final segment is
// usually short, and declaring it at full length makes players seek past the
// end of the asset.
func (m Meta) SegmentDurationAt(i int) float64 {
	if i < m.SegmentCount-1 || m.TotalDuration <= 0 {
		return m.SegmentDuration
	}
	last := m.TotalDuration - m.SegmentDuration*float64(m.SegmentCount-1)
	if last <= 0 || last > m.SegmentDuration {
		return m.SegmentDuration
	}
	return last
}
