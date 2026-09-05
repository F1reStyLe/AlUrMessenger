// Package objectstore обеспечивает подключение к private S3/MinIO bucket.
package objectstore

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Store владеет клиентом и собственным HTTP transport: idle connections закрываются
// при shutdown и не затрагивают глобальный http.DefaultTransport.
type Store struct {
	Client    *minio.Client
	Bucket    string
	region    string
	transport *http.Transport
}

// Open создаёт S3 client без сетевых операций и без автоматического создания bucket.
// TLS использует системные CA и обязательную проверку сертификата/hostname.
func Open(cfg config.Storage, timeout time.Duration) (*Store, error) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: timeout}).DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: timeout,
		ResponseHeaderTimeout: timeout, IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 4}
	client, err := minio.New(cfg.Endpoint, &minio.Options{Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""), Secure: cfg.Secure, Region: cfg.Region, Transport: transport})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, errors.New("MINIO_CONFIG_INVALID")
	}
	return &Store{Client: client, Bucket: cfg.Bucket, region: cfg.Region, transport: transport}, nil
}

// Check требует существующий bucket без bucket policy. Любая непустая policy
// отклоняется консервативно, даже если она выглядит безопасной: анализ IAM не имитируется.
// Права пользователя выдаются IAM policy, а не публичной bucket policy.
func (s *Store) Check(ctx context.Context) error {
	exists, err := s.Client.BucketExists(ctx, s.Bucket)
	if err != nil || !exists {
		return errors.New("MINIO_BUCKET_UNAVAILABLE")
	}
	policy, err := s.Client.GetBucketPolicy(ctx, s.Bucket)
	if err != nil {
		return errors.New("MINIO_POLICY_UNAVAILABLE")
	}
	if strings.TrimSpace(policy) != "" {
		return errors.New("MINIO_BUCKET_POLICY_NOT_EMPTY")
	}
	return nil
}

// Initialize создаёт только отсутствующий bucket и повторяемо проверяет private
// invariant. Существующая policy никогда не перезаписывается без решения оператора.
func (s *Store) Initialize(ctx context.Context) error {
	exists, err := s.Client.BucketExists(ctx, s.Bucket)
	if err != nil {
		return errors.New("MINIO_BUCKET_LOOKUP_FAILED")
	}
	if !exists {
		if err = s.Client.MakeBucket(ctx, s.Bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
			// Конкурентный initializer мог успеть создать bucket; ownership проверяет Check.
			if minio.ToErrorResponse(err).Code != "BucketAlreadyOwnedByYou" {
				return errors.New("MINIO_BUCKET_CREATE_FAILED")
			}
		}
	}
	return s.Check(ctx)
}

// Put writes one already validated object with an exact size and private bucket
// defaults. The caller owns lifecycle metadata and receives no provider details.
func (s *Store) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	result, err := s.Client.PutObject(ctx, s.Bucket, key, body, size, minio.PutObjectOptions{ContentType: contentType, DisableMultipart: size < 5<<20})
	if err != nil || result.Size != size {
		return errors.New("MINIO_PUT_FAILED")
	}
	return nil
}

// Close освобождает idle connections после завершения использующих store handlers.
func (s *Store) Close() { s.transport.CloseIdleConnections() }
