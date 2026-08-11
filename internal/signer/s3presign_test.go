package signer

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func testSigner(t *testing.T, bucket string) *S3 {
	t.Helper()
	client := s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", ""),
		BaseEndpoint: aws.String("https://s3.example.com"),
		UsePathStyle: true,
	})
	s, err := NewS3(client, bucket)
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	return s
}

func TestNewS3Validates(t *testing.T) {
	if _, err := NewS3(nil, "bucket"); err == nil {
		t.Error("NewS3 with nil client = nil error, want error")
	}
	if _, err := NewS3(s3.New(s3.Options{Region: "us-east-1"}), ""); err == nil {
		t.Error("NewS3 with empty bucket = nil error, want error")
	}
}

func TestSignURLStructure(t *testing.T) {
	s := testSigner(t, "videos")
	raw, err := s.Sign(context.Background(), "assets/lecture-01/v0/seg_00001.ts", time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("Sign returned an unparseable URL %q: %v", raw, err)
	}
	if u.Scheme != "https" || u.Host != "s3.example.com" {
		t.Errorf("signed URL host = %s://%s, want https://s3.example.com", u.Scheme, u.Host)
	}
	if want := "/videos/assets/lecture-01/v0/seg_00001.ts"; u.Path != want {
		t.Errorf("signed URL path = %s, want %s", u.Path, want)
	}

	q := u.Query()
	for _, param := range []string{"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-Expires", "X-Amz-Signature"} {
		if q.Get(param) == "" {
			t.Errorf("signed URL is missing %s", param)
		}
	}
	if got := q.Get("X-Amz-Algorithm"); got != "AWS4-HMAC-SHA256" {
		t.Errorf("X-Amz-Algorithm = %s, want AWS4-HMAC-SHA256", got)
	}
}

func TestSignEncodesExpiry(t *testing.T) {
	s := testSigner(t, "videos")
	for _, ttl := range []time.Duration{time.Second, 5 * time.Minute, time.Hour, MaxTTL} {
		raw, err := s.Sign(context.Background(), "assets/a/v0/seg_00001.ts", ttl)
		if err != nil {
			t.Fatalf("Sign(%s): %v", ttl, err)
		}
		u, _ := url.Parse(raw)
		got, err := strconv.Atoi(u.Query().Get("X-Amz-Expires"))
		if err != nil {
			t.Fatalf("X-Amz-Expires is not an integer: %v", err)
		}
		if want := int(ttl.Seconds()); got != want {
			t.Errorf("Sign(%s) encoded expiry %d, want %d", ttl, got, want)
		}
	}
}

func TestSignRejectsBadTTL(t *testing.T) {
	s := testSigner(t, "videos")
	tests := []struct {
		name string
		ttl  time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Second},
		{"beyond sigv4 maximum", MaxTTL + time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Sign(context.Background(), "assets/a/v0/seg_00001.ts", tc.ttl); err == nil {
				t.Fatalf("Sign with %s ttl = nil error, want error", tc.name)
			}
		})
	}
}

func TestSignRejectsEmptyKey(t *testing.T) {
	s := testSigner(t, "videos")
	if _, err := s.Sign(context.Background(), "", time.Hour); err == nil {
		t.Fatal("Sign with empty key = nil error, want error")
	}
}

// Two variants of the same segment must sign to different URLs, or the playlist
// could not steer a session down one branch of the A/B tree.
func TestSignDistinguishesVariants(t *testing.T) {
	s := testSigner(t, "videos")
	v0, err := s.Sign(context.Background(), "assets/a/v0/seg_00001.ts", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := s.Sign(context.Background(), "assets/a/v1/seg_00001.ts", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if v0 == v1 {
		t.Fatal("variant 0 and variant 1 signed to the same URL")
	}
	sig := func(u string) string {
		parsed, _ := url.Parse(u)
		return parsed.Query().Get("X-Amz-Signature")
	}
	if sig(v0) == sig(v1) {
		t.Error("variants share a signature, so the signature does not cover the key")
	}
}

func TestSignIsDeterministicWithinASecond(t *testing.T) {
	s := testSigner(t, "videos")
	ctx := context.Background()
	first, err := s.Sign(ctx, "assets/a/v0/seg_00001.ts", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Sign(ctx, "assets/a/v0/seg_00001.ts", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stamp := func(u string) string {
		parsed, _ := url.Parse(u)
		return parsed.Query().Get("X-Amz-Date")
	}
	if stamp(first) == stamp(second) && first != second {
		t.Error("two signatures over the same key and timestamp differ")
	}
}

func TestSignEscapesAwkwardKeys(t *testing.T) {
	s := testSigner(t, "videos")
	raw, err := s.Sign(context.Background(), "assets/a b/v0/seg 1.ts", time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if strings.Contains(raw, " ") {
		t.Errorf("signed URL contains a raw space: %s", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("unparseable URL: %v", err)
	}
	if want := "/videos/assets/a b/v0/seg 1.ts"; u.Path != want {
		t.Errorf("decoded path = %q, want %q", u.Path, want)
	}
}
