package objectstore

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Config struct {
	Endpoint       string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
}

type S3Store struct {
	client *minio.Client
	bucket string
}

func NewS3Store(ctx context.Context, config Config) (*S3Store, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("object storage endpoint must be an http(s) origin without credentials or path")
	}
	if strings.TrimSpace(config.Bucket) == "" || strings.TrimSpace(config.AccessKey) == "" || strings.TrimSpace(config.SecretKey) == "" {
		return nil, errors.New("object storage bucket and credentials are required")
	}
	region := strings.TrimSpace(config.Region)
	if region == "" {
		region = "us-east-1"
	}
	lookup := minio.BucketLookupAuto
	if config.ForcePathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(config.AccessKey, config.SecretKey, ""),
		Secure:       endpoint.Scheme == "https",
		Region:       region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, errors.New("could not configure object storage client")
	}
	store := &S3Store{client: client, bucket: strings.TrimSpace(config.Bucket)}
	exists, err := client.BucketExists(ctx, store.bucket)
	if err != nil || !exists {
		return nil, errors.New("configured object storage bucket is unavailable")
	}
	return store, nil
}

func (s *S3Store) Put(ctx context.Context, key, contentType string, data []byte) error {
	if s == nil || s.client == nil || !validReferenceImageKey(key) || len(data) == 0 || len(data) > 8<<20 ||
		(contentType != "image/png" && contentType != "image/jpeg") {
		return errors.New("invalid private object write")
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return errors.New("private object write failed")
	}
	return nil
}

func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if s == nil || s.client == nil || !validReferenceImageKey(key) {
		return nil, errors.New("invalid private object read")
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get private object: %w", err)
	}
	return object, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	if s == nil || s.client == nil || !validReferenceImageKey(key) {
		return errors.New("invalid private object deletion")
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return errors.New("private object deletion failed")
	}
	return nil
}

func validReferenceImageKey(key string) bool {
	const prefix = "task-reference-images/"
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	id := strings.TrimPrefix(key, prefix)
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	compact := strings.ReplaceAll(id, "-", "")
	decoded, err := hex.DecodeString(compact)
	return err == nil && len(decoded) == 16
}

var _ interface {
	Put(context.Context, string, string, []byte) error
	Get(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
} = (*S3Store)(nil)
