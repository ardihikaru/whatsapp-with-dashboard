package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
)

// ObjectStore is the media-archival seam HTTP handlers reach MinIO through.
// An interface — not a bare *minio.Client on handler.Deps — for the same
// reason bucketAPI (minio.go) exists: handler tests substitute a fake
// instead of standing up a live MinIO server.
type ObjectStore interface {
	// Put uploads r (size bytes, of contentType) to key, in the configured
	// bucket.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// PresignedGet returns a time-limited URL for key, valid for the
	// configured MinioShareDuration.
	PresignedGet(ctx context.Context, key string) (string, error)
	// Get streams key's bytes back directly, for a caller that needs the
	// content rather than a URL to hand a third party.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// objectAPI is the narrow slice of *minio.Client MinioObjects needs, kept as
// an unexported interface for the same reason bucketAPI (minio.go) is: it
// lets MinioObjects be exercised by internal/storage's own tests without a
// live MinIO server. Every method's signature matches *minio.Client's
// exactly, so a real client satisfies this interface with no adapter — the
// same pattern EnsureBuckets already uses for bucketAPI.
type objectAPI interface {
	PutObject(ctx context.Context, bucketName, objectName string, reader io.Reader, size int64, opts minio.PutObjectOptions) (minio.UploadInfo, error)
	PresignedGetObject(ctx context.Context, bucketName, objectName string, expires time.Duration, reqParams url.Values) (*url.URL, error)
	GetObject(ctx context.Context, bucketName, objectName string, opts minio.GetObjectOptions) (*minio.Object, error)
}

// MinioObjects implements ObjectStore against a real (or, in tests, faked)
// MinIO client, every operation scoped to a single bucket.
type MinioObjects struct {
	client        objectAPI
	bucket        string
	shareDuration time.Duration
}

// NewObjects wraps client for cfg.MinioBucket / cfg.MinioShareDuration.
// client is typed as the unexported objectAPI (not *minio.Client) purely so
// this package's own tests can pass a fake — main.go's
// storage.NewObjects(minioClient, cfg) still compiles unchanged, since
// *minio.Client structurally satisfies objectAPI already.
func NewObjects(client objectAPI, cfg *config.Config) *MinioObjects {
	return &MinioObjects{client: client, bucket: cfg.MinioBucket, shareDuration: cfg.MinioShareDuration}
}

func (m *MinioObjects) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := m.client.PutObject(ctx, m.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("minio: put %q: %w", key, err)
	}
	return nil
}

// PresignedGet is the first consumer of cfg.MinioShareDuration — until this
// method, nothing in the codebase read that config value at all (see
// .env.example's WSM_MINIO_SHARE_DURATION comment, now updated to match).
func (m *MinioObjects) PresignedGet(ctx context.Context, key string) (string, error) {
	u, err := m.client.PresignedGetObject(ctx, m.bucket, key, m.shareDuration, nil)
	if err != nil {
		return "", fmt.Errorf("minio: presigned get %q: %w", key, err)
	}
	return u.String(), nil
}

func (m *MinioObjects) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("minio: get %q: %w", key, err)
	}
	return obj, nil
}

// ObjectKey builds the key a media send archives under:
// media/{session_id}/{yyyy}/{mm}/{message_id}{ext}.
//
// The result is STORED on wa_messages.media_object_key by the caller
// (Phase 6's handler) rather than re-derived from session/message ids on
// every read. That is deliberate: a future change to this layout (e.g.
// adding a day component, or hashing the session id) must not orphan
// objects a past send already archived under the old layout — a row always
// carries the exact key it was written with, so reads never have to guess
// which layout version produced it.
func ObjectKey(sessionID, messageID string, at time.Time, ext string) string {
	return fmt.Sprintf("media/%s/%04d/%02d/%s%s",
		sessionID, at.Year(), at.Month(), messageID, ext)
}
