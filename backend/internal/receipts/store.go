package receipts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store struct {
	client *minio.Client
	bucket string
	region string
}

func New(endpoint, accessKey, secretKey, bucket, region string) (*Store, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" ||
		accessKey == "" || secretKey == "" || bucket == "" || region == "" {
		return nil, errors.New("invalid receipt storage configuration")
	}
	client, err := minio.New(parsed.Host, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: parsed.Scheme == "https", Region: region, BucketLookup: minio.BucketLookupPath})
	if err != nil {
		return nil, errors.New("invalid receipt storage configuration")
	}
	return &Store{client: client, bucket: bucket, region: region}, nil
}

func (s *Store) CheckBucket(ctx context.Context, create bool) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return errors.New("could not connect to receipt storage")
	}
	if exists {
		return nil
	}
	if create {
		if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err == nil {
			return nil
		}
		exists, err := s.client.BucketExists(ctx, s.bucket)
		if err == nil && exists {
			return nil
		}
	}
	return errors.New("private receipt bucket is unavailable")
}

func (s *Store) Put(ctx context.Context, key, contentType string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType, DisableMultipart: true})
	return err
}

func (s *Store) Get(ctx context.Context, key string, size int64) ([]byte, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer object.Close()
	data, err := io.ReadAll(io.LimitReader(object, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, errors.New("unexpected receipt object size")
	}
	return data, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
