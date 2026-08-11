package storage

import (
	"fmt"
	"regexp"
)

// Asset ids reach this package from the HTTP API, so they are constrained
// rather than trusted: anything outside this set could escape the prefix a
// signing key is scoped to.
var assetIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

func ValidateAssetID(id string) error {
	if !assetIDPattern.MatchString(id) {
		return fmt.Errorf("storage: invalid asset id %q", id)
	}
	return nil
}

func AssetPrefix(assetID string) string { return "assets/" + assetID + "/" }

func MetaKey(assetID string) string { return AssetPrefix(assetID) + "meta.json" }

func BaseManifestKey(assetID string) string { return AssetPrefix(assetID) + "manifest.base.m3u8" }

// SegmentKey numbers segments from one, matching the seg_00001.ts layout in
// SPEC 7. Only the variant prefix differs between the two copies, which is what
// keeps the CDN footprint at 2x rather than Nx.
func SegmentKey(assetID string, variant uint8, index int) (string, error) {
	if err := ValidateAssetID(assetID); err != nil {
		return "", err
	}
	if variant > 1 {
		return "", fmt.Errorf("storage: variant must be 0 or 1, got %d", variant)
	}
	if index < 1 {
		return "", fmt.Errorf("storage: segment index starts at 1, got %d", index)
	}
	return fmt.Sprintf("%sv%d/seg_%05d.ts", AssetPrefix(assetID), variant, index), nil
}
