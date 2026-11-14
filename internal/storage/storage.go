// Package storage = penyimpanan objek (MinIO/S3) di balik antarmuka; tes memakai Memory.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var ErrNotFound = errors.New("objek tidak ditemukan")

type Info struct {
	Size        int64
	ContentType string
}

type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, Info, error)
	Delete(ctx context.Context, key string) error
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// ---- MinIO ----

type MinIO struct {
	c      *minio.Client
	bucket string
}

// NewMinIO membuka klien dan memastikan bucket ada (gagal keras bila tidak terjangkau).
func NewMinIO(ctx context.Context, endpoint, accessKey, secretKey, bucket string, ssl bool) (*MinIO, error) {
	c, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: ssl})
	if err != nil {
		return nil, fmt.Errorf("minio: %w", err)
	}
	ok, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("minio: cek bucket: %w", err)
	}
	if !ok {
		if err := c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("minio: buat bucket: %w", err)
		}
	}
	return &MinIO{c: c, bucket: bucket}, nil
}

func (m *MinIO) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := m.c.PutObject(ctx, m.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (m *MinIO) Get(ctx context.Context, key string) (io.ReadCloser, Info, error) {
	obj, err := m.c.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, Info{}, err
	}
	st, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, Info{}, ErrNotFound
		}
		return nil, Info{}, err
	}
	return obj, Info{Size: st.Size, ContentType: st.ContentType}, nil
}

func (m *MinIO) Delete(ctx context.Context, key string) error {
	return m.c.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{})
}

func (m *MinIO) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	u, err := m.c.PresignedGetObject(ctx, m.bucket, key, ttl, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// ---- Memory ----

type memObj struct {
	data []byte
	ct   string
}

// Memory = implementasi dalam memori untuk tes.
type Memory struct {
	mu   sync.Mutex
	objs map[string]memObj
}

func NewMemory() *Memory { return &Memory{objs: map[string]memObj{}} }

func (m *Memory) Put(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.objs[key] = memObj{b, contentType}
	m.mu.Unlock()
	return nil
}

func (m *Memory) Get(_ context.Context, key string) (io.ReadCloser, Info, error) {
	m.mu.Lock()
	o, ok := m.objs[key]
	m.mu.Unlock()
	if !ok {
		return nil, Info{}, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(o.data)), Info{Size: int64(len(o.data)), ContentType: o.ct}, nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.objs, key)
	m.mu.Unlock()
	return nil
}

func (m *Memory) PresignGet(_ context.Context, key string, _ time.Duration) (string, error) {
	return "memory://" + key, nil
}

// Keys = daftar kunci (tes).
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.objs))
	for k := range m.objs {
		out = append(out, k)
	}
	return out
}
