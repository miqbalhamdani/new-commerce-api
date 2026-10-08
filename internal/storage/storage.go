// Package storage is the object store: R2 in production, MinIO in
// development (BR-051, BR-053). Image bytes never pass through the API -- the
// browser uploads with a presigned PUT, the API only signs and checks.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"

	"github.com/miqbalhamdani/new-commerce-api/internal/platform/config"
)

// ErrNotFound is an object that was never uploaded (or is gone).
var ErrNotFound = errors.New("object not found")

// Store is one bucket.
type Store struct {
	client     *minio.Client
	bucket     string
	publicBase string
}

// Config is where the bucket lives.
type Config struct {
	Endpoint, AccessKey, SecretKey, Bucket, PublicBaseURL string
	UseSSL                                                bool
}

func New(cfg Config) (*Store, error) {
	c, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""), Secure: cfg.UseSSL, Region: "auto"})
	if err != nil {
		return nil, fmt.Errorf("object store client: %w", err)
	}
	return &Store{client: c, bucket: cfg.Bucket, publicBase: strings.TrimRight(cfg.PublicBaseURL, "/")}, nil
}

// PresignPut is a URL the browser PUTs the raw bytes to.
func (s *Store) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.client.PresignedPutObject(ctx, s.bucket, key, ttl)
	if err != nil {
		return "", fmt.Errorf("presign put: %w", err)
	}
	return u.String(), nil
}

// PresignGet is a short-lived download URL for a private object (BR-063).
func (s *Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, url.Values{})
	if err != nil {
		return "", fmt.Errorf("presign get: %w", err)
	}
	return u.String(), nil
}

// Head reports an object's content type and size; ErrNotFound if it was never
// uploaded.
func (s *Store) Head(ctx context.Context, key string) (contentType string, size int64, err error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return "", 0, ErrNotFound
		}
		return "", 0, fmt.Errorf("head %s: %w", key, err)
	}
	return info.ContentType, info.Size, nil
}

// Get streams an object.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	return obj, nil
}

// Put writes an object.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// Delete removes an object; a missing one is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

// PublicURL is a product image's URL on the image domain (BR-050, BR-053):
// built on read, never stored.
func (s *Store) PublicURL(key string) string { return s.publicBase + "/" + key }

// Expiry is how long temporary files live, by key prefix (BR-053). R2 gets the
// same two lifecycle rules at provisioning (P1-045).
var Expiry = map[string]int{"jobs/": 30, "exports/": 7}

// EnsureBucket creates the bucket, lets anyone read product images and sets
// the Expiry rules, the way the R2 bucket and image domain do in production
// (P1-045). For development; production buckets are provisioned, not created
// by the app.
func (s *Store) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("bucket exists: %w", err)
	}
	if !exists {
		if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("make bucket: %w", err)
		}
	}
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},
		"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*/products/*"]}]}`, s.bucket)
	if err := s.client.SetBucketPolicy(ctx, s.bucket, policy); err != nil {
		return fmt.Errorf("bucket policy: %w", err)
	}
	rules := lifecycle.NewConfiguration()
	for prefix, days := range Expiry {
		rules.Rules = append(rules.Rules, lifecycle.Rule{ID: "expire-" + strings.TrimSuffix(prefix, "/"), Status: "Enabled",
			RuleFilter: lifecycle.Filter{Prefix: prefix}, Expiration: lifecycle.Expiration{Days: lifecycle.ExpirationDays(days)}})
	}
	if err := s.client.SetBucketLifecycle(ctx, s.bucket, rules); err != nil {
		return fmt.Errorf("bucket lifecycle: %w", err)
	}
	return nil
}

// FromEnv is the store the environment names (config.S3*).
func FromEnv() (*Store, error) {
	return New(Config{Endpoint: config.S3Endpoint(), AccessKey: config.S3AccessKey(),
		SecretKey: config.S3SecretKey(), Bucket: config.S3Bucket(), PublicBaseURL: config.ImageBaseURL(),
		UseSSL: config.S3UseSSL()})
}
