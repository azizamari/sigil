// Package manifest generates the per-session HLS playlist.
//
// The playlist is the personalised artifact: segments are shared across the
// whole viewer population, and which of the two variants a session receives is
// decided here, in string assembly, with no per-request video processing.
package manifest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/azizamari/sigil/internal/storage"
)

// Signer is declared here rather than imported so this package depends on the
// one method it uses.
type Signer interface {
	Sign(ctx context.Context, key string, ttl time.Duration) (string, error)
}

type Options struct {
	// SegmentTTL must outlast the longest plausible viewing session or playback
	// breaks part way through for slow watchers.
	SegmentTTL time.Duration
	// SessionData is emitted as EXT-X-SESSION-DATA. The overlay string travels
	// here and is never stored server-side.
	SessionData map[string]string
}

type Builder struct {
	signer Signer
}

func NewBuilder(s Signer) (*Builder, error) {
	if s == nil {
		return nil, errors.New("manifest: nil signer")
	}
	return &Builder{signer: s}, nil
}

// Build renders a playlist for one session. sequence carries the variant bit
// per segment and must be nil for an unwatermarked asset.
//
// Every segment is signed with identical work regardless of its bit, so
// generation time does not vary with the sequence.
func (b *Builder) Build(ctx context.Context, meta storage.Meta, sequence []uint8, opts Options) (string, error) {
	if err := meta.Validate(); err != nil {
		return "", err
	}
	if opts.SegmentTTL <= 0 {
		return "", errors.New("manifest: segment ttl must be positive")
	}
	if meta.Watermarked && len(sequence) < meta.SegmentCount {
		return "", fmt.Errorf("manifest: sequence covers %d of %d segments",
			len(sequence), meta.SegmentCount)
	}

	urls := make([]string, meta.SegmentCount)
	for i := range meta.SegmentCount {
		key, err := b.segmentKey(meta, sequence, i)
		if err != nil {
			return "", err
		}
		signed, err := b.signer.Sign(ctx, key, opts.SegmentTTL)
		if err != nil {
			return "", fmt.Errorf("manifest: sign segment %d: %w", i+1, err)
		}
		urls[i] = signed
	}
	return render(meta, urls, opts.SessionData), nil
}

func (b *Builder) segmentKey(meta storage.Meta, sequence []uint8, i int) (string, error) {
	if !meta.Watermarked {
		return storage.FlatSegmentKey(meta.AssetID, i+1)
	}
	return storage.SegmentKey(meta.AssetID, sequence[i], i+1)
}

func render(meta storage.Meta, urls []string, sessionData map[string]string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", meta.TargetDuration())
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")

	for _, id := range sortedKeys(sessionData) {
		fmt.Fprintf(&b, "#EXT-X-SESSION-DATA:DATA-ID=%q,VALUE=%q\n", id, escapeAttr(sessionData[id]))
	}

	for i, u := range urls {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n", meta.SegmentDurationAt(i))
		b.WriteString(u)
		b.WriteByte('\n')
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// Quoted attribute values may not contain carriage returns, line feeds or
// double quotes; overlay text is arbitrary and unvalidated, so it is stripped
// rather than trusted.
func escapeAttr(v string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\r', '\n':
			return -1
		}
		return r
	}, v)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
