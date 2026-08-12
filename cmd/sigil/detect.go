package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/azizamari/sigil/internal/detect"
	"github.com/azizamari/sigil/internal/embed"
	"github.com/azizamari/sigil/internal/storage"
)

func runDetect(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("detect", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var (
		assetID   = fs.String("asset-id", "", "asset the leak came from")
		issuedRaw = fs.String("sessions", "", "JSON file of issued sessions: [{\"session_id\":..,\"payload_id\":..}]")
		metaPath  = fs.String("meta", "", "local meta.json; otherwise read from the bucket")
		threshold = fs.Float64("threshold", detect.DefaultThreshold, "confidence a match must reach")
		asJSON    = fs.Bool("json", false, "emit the result as JSON")
		bucket    = fs.String("s3-bucket", "", "bucket holding the asset")
		endpoint  = fs.String("s3-endpoint", "", "custom S3 endpoint")
		region    = fs.String("s3-region", "", "S3 region")
		pathStyle = fs.Bool("s3-path-style", false, "path-style addressing")
	)

	var leak string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		leak, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if leak == "" && fs.NArg() == 1 {
		leak = fs.Arg(0)
	}
	if leak == "" {
		return errors.New("usage: sigil detect <leaked file> --asset-id <id> --sessions <file>")
	}

	meta, err := loadMeta(ctx, *metaPath, *assetID, storage.Config{
		Bucket: *bucket, Endpoint: *endpoint, Region: *region, PathStyle: *pathStyle,
	})
	if err != nil {
		return err
	}
	issued, err := loadIssued(*issuedRaw)
	if err != nil {
		return err
	}

	d := &detect.Detector{Analyzer: embed.NewFFmpeg(), Threshold: *threshold}
	res, err := d.Run(ctx, leak, meta, issued)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	if !res.Matched {
		fmt.Fprintf(stdout, "no match\nconfidence: %.3f (threshold %.2f, null peak %.3f)\n",
			res.Confidence, *threshold, res.NullPeak)
		fmt.Fprintln(stdout, "\nThis is not evidence of an unmarked source: it means no issued session")
		fmt.Fprintln(stdout, "fits this file well enough to name anyone.")
		return nil
	}

	fmt.Fprintf(stdout, "session_id: %s  confidence: %.3f  bits_recovered: %d/%d\n",
		res.SessionID, res.Confidence, res.BitsRecovered, res.BitsTotal)
	fmt.Fprintf(stdout, "null peak:  %.3f (best score reached by a sequence that was never issued)\n", res.NullPeak)
	fmt.Fprintln(stdout, "\nDetection is evidence, not proof. Resolve the session id through your own")
	fmt.Fprintln(stdout, "records, and read the confidence alongside the documented false-positive rate.")
	return nil
}

func loadMeta(ctx context.Context, path, assetID string, cfg storage.Config) (storage.Meta, error) {
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return storage.Meta{}, fmt.Errorf("read meta: %w", err)
		}
		var m storage.Meta
		if err := json.Unmarshal(raw, &m); err != nil {
			return storage.Meta{}, fmt.Errorf("decode meta: %w", err)
		}
		return m, m.Validate()
	}
	if assetID == "" {
		return storage.Meta{}, errors.New("--asset-id or --meta is required")
	}
	store, err := storage.New(ctx, cfg)
	if err != nil {
		return storage.Meta{}, err
	}
	return store.GetMeta(ctx, assetID)
}

func loadIssued(path string) ([]detect.Issued, error) {
	if path == "" {
		return nil, errors.New("--sessions is required: detection matches against the sessions actually issued")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	var issued []detect.Issued
	if err := json.Unmarshal(raw, &issued); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}
	if len(issued) == 0 {
		return nil, errors.New("sessions file is empty")
	}
	return issued, nil
}
