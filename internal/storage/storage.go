// Package storage reads and writes assets in any S3-compatible bucket.
//
// The S3 API is the abstraction; there is no interface over multiple provider
// SDKs. Credentials come from the AWS default chain and are never accepted over
// sigil's own API.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type Config struct {
	Bucket   string
	Endpoint string
	Region   string
	// PathStyle is required by MinIO and rejected by R2. Getting it wrong is
	// the most common non-AWS integration failure.
	PathStyle bool
}

func (c Config) validate() error {
	if c.Bucket == "" {
		return errors.New("storage: bucket is required")
	}
	if c.Endpoint != "" && c.Region == "" {
		return errors.New("storage: a custom endpoint also needs a region")
	}
	return nil
}

// Client is deliberately a value rather than a package singleton so a future
// multi-tenant deployment can resolve one per asset without a rewrite.
type Client struct {
	api    *s3.Client
	bucket string
}

func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("storage: load aws config: %w", err)
	}
	return NewFromAWS(awsCfg, cfg), nil
}

func NewFromAWS(awsCfg aws.Config, cfg Config) *Client {
	api := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.PathStyle
	})
	return &Client{api: api, bucket: cfg.Bucket}
}

func (c *Client) Bucket() string  { return c.bucket }
func (c *Client) API() *s3.Client { return c.api }

func (c *Client) Put(ctx context.Context, key string, body io.Reader, contentType string) error {
	if key == "" {
		return errors.New("storage: empty key")
	}
	in := &s3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if _, err := c.api.PutObject(ctx, in); err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	return nil
}

func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: get %q: %w", key, err)
	}
	return out.Body, nil
}

func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	_, err := c.api.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	var missing *types.NotFound
	if errors.As(err, &missing) {
		return false, nil
	}
	return false, fmt.Errorf("storage: head %q: %w", key, err)
}

func (c *Client) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(c.api, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("storage: list %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}
	return keys, nil
}
