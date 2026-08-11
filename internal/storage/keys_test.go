package storage

import (
	"strings"
	"testing"
)

func TestValidateAssetID(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		valid bool
	}{
		{"simple", "lecture-01", true},
		{"dots and underscores", "course.1_intro", true},
		{"single character", "a", true},
		{"empty", "", false},
		{"leading dash", "-lecture", false},
		{"path traversal", "../../etc/passwd", false},
		{"embedded slash", "course/lecture", false},
		{"parent reference", "..", false},
		{"space", "lecture 01", false},
		{"too long", strings.Repeat("a", 129), false},
		{"null byte", "lecture\x00", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAssetID(tc.id)
			if tc.valid && err != nil {
				t.Errorf("ValidateAssetID(%q) = %v, want nil", tc.id, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("ValidateAssetID(%q) = nil, want error", tc.id)
			}
		})
	}
}

func TestSegmentKey(t *testing.T) {
	tests := []struct {
		name    string
		asset   string
		variant uint8
		index   int
		want    string
		wantErr bool
	}{
		{name: "variant 0", asset: "lecture-01", variant: 0, index: 1, want: "assets/lecture-01/v0/seg_00001.ts"},
		{name: "variant 1", asset: "lecture-01", variant: 1, index: 1, want: "assets/lecture-01/v1/seg_00001.ts"},
		{name: "zero padded", asset: "a", variant: 0, index: 42, want: "assets/a/v0/seg_00042.ts"},
		{name: "five digits", asset: "a", variant: 0, index: 99999, want: "assets/a/v0/seg_99999.ts"},
		{name: "bad variant", asset: "a", variant: 2, index: 1, wantErr: true},
		{name: "zero index", asset: "a", variant: 0, index: 0, wantErr: true},
		{name: "traversal in asset id", asset: "../secrets", variant: 0, index: 1, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SegmentKey(tc.asset, tc.variant, tc.index)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SegmentKey(%q, %d, %d) = nil error, want error", tc.asset, tc.variant, tc.index)
				}
				return
			}
			if err != nil {
				t.Fatalf("SegmentKey: %v", err)
			}
			if got != tc.want {
				t.Errorf("SegmentKey = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKeysStayUnderAssetPrefix(t *testing.T) {
	const id = "lecture-01"
	prefix := AssetPrefix(id)
	keys := []string{MetaKey(id), BaseManifestKey(id)}
	seg, err := SegmentKey(id, 1, 7)
	if err != nil {
		t.Fatal(err)
	}
	keys = append(keys, seg)
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			t.Errorf("key %q escapes the asset prefix %q", k, prefix)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "aws defaults", cfg: Config{Bucket: "videos"}},
		{name: "minio", cfg: Config{Bucket: "videos", Endpoint: "http://localhost:9000", Region: "us-east-1", PathStyle: true}},
		{name: "no bucket", cfg: Config{}, wantErr: true},
		{name: "endpoint without region", cfg: Config{Bucket: "videos", Endpoint: "http://localhost:9000"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.validate()
			if tc.wantErr != (err != nil) {
				t.Errorf("validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
