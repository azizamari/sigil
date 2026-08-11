//go:build integration

package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/azizamari/sigil/internal/signer"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/minio"
)

const testBucket = "sigil-test"

func startMinIO(t *testing.T) (*Client, *s3.Client) {
	t.Helper()
	ctx := context.Background()

	container, err := minio.Run(ctx, "minio/minio:RELEASE.2025-04-22T22-12-26Z")
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	testcontainers.CleanupContainer(t, container)

	endpoint, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	awsCfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(container.Username, container.Password, ""),
	}
	cfg := Config{
		Bucket:    testBucket,
		Endpoint:  "http://" + endpoint,
		Region:    "us-east-1",
		PathStyle: true,
	}
	client := NewFromAWS(awsCfg, cfg)

	if _, err := client.API().CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(testBucket),
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return client, client.API()
}

func TestPutGetListExists(t *testing.T) {
	client, _ := startMinIO(t)
	ctx := context.Background()

	key, err := SegmentKey("lecture-01", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("fake transport stream")
	if err := client.Put(ctx, key, bytes.NewReader(payload), "video/mp2t"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	body, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("Get returned %q, want %q", got, payload)
	}

	ok, err := client.Exists(ctx, key)
	if err != nil || !ok {
		t.Errorf("Exists(%q) = %v, %v; want true, nil", key, ok, err)
	}
	missing, err := client.Exists(ctx, "assets/lecture-01/v0/seg_09999.ts")
	if err != nil {
		t.Errorf("Exists on a missing key returned an error: %v", err)
	}
	if missing {
		t.Error("Exists reported a missing object as present")
	}

	keys, err := client.List(ctx, AssetPrefix("lecture-01"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != key {
		t.Errorf("List = %v, want [%s]", keys, key)
	}
}

// SPEC 8.7: the bucket stays private and the pre-signed URL is the only access
// mechanism, so an unsigned fetch must fail.
func TestPresignedURLResolvesAndUnsignedDoesNot(t *testing.T) {
	client, api := startMinIO(t)
	ctx := context.Background()

	key, err := SegmentKey("lecture-01", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("variant one segment")
	if err := client.Put(ctx, key, bytes.NewReader(payload), "video/mp2t"); err != nil {
		t.Fatal(err)
	}

	s, err := signer.NewS3(api, testBucket)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := s.Sign(ctx, key, time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	got, status, err := fetch(signed)
	if err != nil {
		t.Fatalf("fetch signed URL: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("signed URL returned %d, want 200", status)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("signed URL returned %q, want %q", got, payload)
	}

	unsigned := fmt.Sprintf("http://%s/%s/%s", hostOf(signed), testBucket, key)
	if _, status, err := fetch(unsigned); err != nil {
		t.Fatalf("fetch unsigned URL: %v", err)
	} else if status == http.StatusOK {
		t.Error("unsigned fetch succeeded; the bucket is publicly readable")
	}
}

func TestTamperedSignatureRejected(t *testing.T) {
	client, api := startMinIO(t)
	ctx := context.Background()

	key, _ := SegmentKey("lecture-01", 0, 5)
	if err := client.Put(ctx, key, bytes.NewReader([]byte("payload")), "video/mp2t"); err != nil {
		t.Fatal(err)
	}
	s, _ := signer.NewS3(api, testBucket)
	signed, err := s.Sign(ctx, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		url  string
	}{
		{"flipped signature byte", flipSignature(signed)},
		{"key swapped to the other variant", swapVariant(signed)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, status, err := fetch(tc.url)
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if status == http.StatusOK {
				t.Errorf("tampered URL returned 200: %s", tc.url)
			}
		})
	}
}

func TestExpiredURLRejected(t *testing.T) {
	client, api := startMinIO(t)
	ctx := context.Background()

	key, _ := SegmentKey("lecture-01", 0, 9)
	if err := client.Put(ctx, key, bytes.NewReader([]byte("payload")), "video/mp2t"); err != nil {
		t.Fatal(err)
	}
	s, _ := signer.NewS3(api, testBucket)
	signed, err := s.Sign(ctx, key, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(2 * time.Second)
	_, status, err := fetch(signed)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if status == http.StatusOK {
		t.Error("expired URL still returned 200")
	}
}

// HLS seeking issues range requests; without them playback starts fine and
// breaks on the first seek.
func TestRangeRequestOnSignedURL(t *testing.T) {
	client, api := startMinIO(t)
	ctx := context.Background()

	key, _ := SegmentKey("lecture-01", 0, 11)
	payload := []byte("0123456789abcdef")
	if err := client.Put(ctx, key, bytes.NewReader(payload), "video/mp2t"); err != nil {
		t.Fatal(err)
	}
	s, _ := signer.NewS3(api, testBucket)
	signed, err := s.Sign(ctx, key, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signed, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=4-7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range request returned %d, want 206", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if want := "4567"; string(body) != want {
		t.Errorf("range request returned %q, want %q", body, want)
	}
	if resp.Header.Get("Content-Range") == "" {
		t.Error("range response is missing Content-Range")
	}
}

func TestGetMissingKeyErrors(t *testing.T) {
	client, _ := startMinIO(t)
	if _, err := client.Get(context.Background(), "assets/nope/v0/seg_00001.ts"); err == nil {
		t.Fatal("Get on a missing key = nil error, want error")
	}
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func flipSignature(raw string) string {
	u, _ := url.Parse(raw)
	q := u.Query()
	sig := []byte(q.Get("X-Amz-Signature"))
	if sig[len(sig)-1] == 'a' {
		sig[len(sig)-1] = 'b'
	} else {
		sig[len(sig)-1] = 'a'
	}
	q.Set("X-Amz-Signature", string(sig))
	u.RawQuery = q.Encode()
	return u.String()
}

func swapVariant(raw string) string {
	u, _ := url.Parse(raw)
	u.Path = strings.Replace(u.Path, "/v0/", "/v1/", 1)
	return u.String()
}

func fetch(url string) ([]byte, int, error) {
	resp, err := http.Get(url) //nolint:gosec // test-local URLs
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}
