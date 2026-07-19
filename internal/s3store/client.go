package s3store

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Options configures a Client. Credentials are plaintext here — decrypting
// the s3_config table's access_key_encrypted/secret_key_encrypted (under
// the Master Key, per FR-S3-02) is the caller's job, the same convention
// internal/imapclient's ConnectOptions uses for IMAP passwords.
type Options struct {
	Endpoint        string // custom/S3-compatible endpoint (MinIO); empty = real AWS S3
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	PathStyle       bool // MinIO and most S3-compatible servers require this
	TLSSkipVerify   bool
}

// Client wraps an aws-sdk-go-v2 S3 client bound to a single bucket.
type Client struct {
	s3     *s3.Client
	bucket string
}

// NewClient builds a Client from Options. It does not itself verify the
// bucket is reachable — callers wanting a connectivity check should call
// EnsureBucket or a HeadBucket-based test-connection.
func NewClient(opts Options) (*Client, error) {
	if opts.Bucket == "" {
		return nil, errors.New("s3store: bucket is required")
	}

	var httpClient *http.Client
	if opts.TLSSkipVerify {
		httpClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in, self-hosted S3-compatible endpoints only
			},
		}
	}

	s3Opts := s3.Options{
		Region:       opts.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(opts.AccessKeyID, opts.SecretAccessKey, ""),
		UsePathStyle: opts.PathStyle,
	}
	if opts.Endpoint != "" {
		s3Opts.BaseEndpoint = aws.String(opts.Endpoint)
	}
	if httpClient != nil {
		s3Opts.HTTPClient = httpClient
	}

	return &Client{s3: s3.New(s3Opts), bucket: opts.Bucket}, nil
}

// Ping confirms the configured bucket is reachable and accessible with
// the current credentials, without creating anything — the safe
// test-connection check for a real, user-owned bucket. Unlike
// EnsureBucket (which the MinIO test harness relies on to provision a
// fresh bucket), Ping never issues CreateBucket: silently creating a
// bucket in someone's real S3 account as a side effect of "test
// connection" would be a surprising, unwanted mutation.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)}); err != nil {
		return fmt.Errorf("s3store: bucket %q not reachable: %w", c.bucket, err)
	}
	return nil
}

// EnsureBucket creates the configured bucket if it doesn't already exist.
// MinIO (unlike real AWS S3, where buckets are provisioned out-of-band)
// requires this for a freshly started test container; it's harmless
// against real S3 too since it treats an existing owned bucket as success.
func (c *Client) EnsureBucket(ctx context.Context) error {
	_, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.bucket)})
	if err == nil {
		return nil
	}
	_, err = c.s3.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(c.bucket)})
	if err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		if errors.As(err, &owned) {
			return nil
		}
		return fmt.Errorf("s3store: creating bucket %q: %w", c.bucket, err)
	}
	return nil
}

// Put uploads body under key, along with metadata (arbitrary key/value
// pairs, e.g. encryption IV/tag/sha256 in a later step) as S3 object
// metadata headers. It returns the response ETag.
func (c *Client) Put(ctx context.Context, key string, body io.Reader, metadata map[string]string) (etag string, err error) {
	out, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   aws.String(c.bucket),
		Key:      aws.String(key),
		Body:     body,
		Metadata: metadata,
	})
	if err != nil {
		return "", fmt.Errorf("s3store: putting %q: %w", key, err)
	}
	return aws.ToString(out.ETag), nil
}

// Get downloads the object at key. The caller must close the returned
// ReadCloser. Metadata holds whatever was set on Put (lowercased keys, per
// S3's own convention for user metadata headers).
func (c *Client) Get(ctx context.Context, key string) (body io.ReadCloser, metadata map[string]string, err error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("s3store: getting %q: %w", key, err)
	}
	return out.Body, out.Metadata, nil
}

// Delete removes the object at key. Deleting an already-absent key is not
// an error — S3's DeleteObject is idempotent by design.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3store: deleting %q: %w", key, err)
	}
	return nil
}
