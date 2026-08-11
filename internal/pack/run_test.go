package pack

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/azizamari/sigil/internal/embed"
	"github.com/azizamari/sigil/internal/storage"
)

// A local object store keeps this runnable wherever ffmpeg is installed; the
// real S3 behaviour is covered by the MinIO suite.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/videos/")
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		body, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for k := range f.objects {
		out = append(out, k)
	}
	return out
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("needs ffmpeg; this is an L2 test")
	}
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
}

func TestRunPackagesBothVariantsAndWritesMeta(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=960x540:rate=15:duration=10",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "21", "-pix_fmt", "yuv420p", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, out)
	}

	fake := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	store := storage.NewFromAWS(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", ""),
	}, storage.Config{Bucket: "videos", Endpoint: srv.URL, Region: "us-east-1", PathStyle: true})

	embedder := embed.NewFFmpeg()
	embedder.Encode = []string{"-c:v", "libx264", "-preset", "ultrafast", "-crf", "21", "-pix_fmt", "yuv420p"}

	packer := &Packer{Embedder: embedder, Storage: store, Log: slog.New(slog.DiscardHandler)}
	result, err := packer.Run(context.Background(), src, Options{
		AssetID:         "lecture-01",
		SegmentDuration: 0.5,
		Sessions:        8,
		WorkDir:         filepath.Join(dir, "work"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.Meta.SegmentCount < 15 {
		t.Fatalf("packaged %d segments, want at least 15", result.Meta.SegmentCount)
	}
	if want := result.Meta.SegmentCount * 2; result.Uploaded != want {
		t.Errorf("uploaded %d objects, want %d (both variants)", result.Uploaded, want)
	}

	// Both variant prefixes must be fully populated: a session whose sequence
	// asks for a missing segment would simply fail to play.
	keys := fake.keys()
	for _, variant := range []uint8{0, 1} {
		for i := 1; i <= result.Meta.SegmentCount; i++ {
			key, err := storage.SegmentKey("lecture-01", variant, i)
			if err != nil {
				t.Fatal(err)
			}
			if !contains(keys, key) {
				t.Fatalf("missing object %s", key)
			}
		}
	}

	raw, ok := fake.objects[storage.MetaKey("lecture-01")]
	if !ok {
		t.Fatal("meta.json was not written")
	}
	var meta storage.Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("meta.json is not valid JSON: %v", err)
	}
	if err := meta.Validate(); err != nil {
		t.Errorf("stored meta does not validate: %v", err)
	}
	if meta.Embed == nil || meta.Codebook == nil {
		t.Fatal("meta.json must carry both embed and codebook parameters")
	}
	if meta.Codebook.SegmentCount != meta.SegmentCount {
		t.Errorf("codebook covers %d segments but the asset has %d",
			meta.Codebook.SegmentCount, meta.SegmentCount)
	}
	if !meta.Watermarked {
		t.Error("packaged asset must be marked watermarked")
	}
}

func TestRunRequiresDependencies(t *testing.T) {
	p := &Packer{}
	if _, err := p.Run(context.Background(), "src.mp4", Options{AssetID: "a"}); err == nil {
		t.Fatal("Run without an embedder or storage = nil error, want error")
	}
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
