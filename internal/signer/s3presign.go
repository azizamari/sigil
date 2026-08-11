package signer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// MaxTTL is the longest expiry SigV4 permits. Segment URLs must outlast the
// longest plausible viewing session, so this ceiling is a real constraint on
// how long an asset can be watched from a single minted playlist.
const MaxTTL = 7 * 24 * time.Hour

type S3 struct {
	presign *s3.PresignClient
	bucket  string
}

func NewS3(client *s3.Client, bucket string) (*S3, error) {
	if client == nil {
		return nil, errors.New("signer: nil s3 client")
	}
	if bucket == "" {
		return nil, errors.New("signer: empty bucket")
	}
	return &S3{presign: s3.NewPresignClient(client), bucket: bucket}, nil
}

// Sign takes a context because credential resolution may still refresh against
// IMDS or STS even though the signature itself is computed locally.
func (s *S3) Sign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	switch {
	case key == "":
		return "", errors.New("signer: empty key")
	case ttl <= 0:
		return "", fmt.Errorf("signer: ttl must be positive, got %s", ttl)
	case ttl > MaxTTL:
		return "", fmt.Errorf("signer: ttl %s exceeds the SigV4 maximum of %s", ttl, MaxTTL)
	}

	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("signer: presign %q: %w", key, err)
	}
	return req.URL, nil
}
