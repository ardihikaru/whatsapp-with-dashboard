package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
)

// fakeObjectAPI records every call instead of talking to a network, so
// MinioObjects is testable without a live MinIO server — mirrors
// fakeBucketAPI in minio_test.go.
type fakeObjectAPI struct {
	// putBucket/putKey/putSize/putContentType/putBody record PutObject's
	// most recent call.
	putBucket, putKey, putContentType string
	putSize                           int64
	putBody                           []byte
	putErr                            error

	// presignBucket/presignKey/presignExpires record PresignedGetObject's
	// most recent call.
	presignBucket, presignKey string
	presignExpires            time.Duration
	presignErr                error

	getErr error
}

func (f *fakeObjectAPI) PutObject(_ context.Context, bucketName, objectName string, reader io.Reader, size int64, opts minio.PutObjectOptions) (minio.UploadInfo, error) {
	if f.putErr != nil {
		return minio.UploadInfo{}, f.putErr
	}
	body, _ := io.ReadAll(reader)
	f.putBucket, f.putKey, f.putSize, f.putContentType, f.putBody =
		bucketName, objectName, size, opts.ContentType, body
	return minio.UploadInfo{Bucket: bucketName, Key: objectName, Size: size}, nil
}

func (f *fakeObjectAPI) PresignedGetObject(_ context.Context, bucketName, objectName string, expires time.Duration, _ url.Values) (*url.URL, error) {
	if f.presignErr != nil {
		return nil, f.presignErr
	}
	f.presignBucket, f.presignKey, f.presignExpires = bucketName, objectName, expires
	return &url.URL{Scheme: "https", Host: "minio.example.com", Path: "/" + bucketName + "/" + objectName}, nil
}

func (f *fakeObjectAPI) GetObject(_ context.Context, _, _ string, _ minio.GetObjectOptions) (*minio.Object, error) {
	return nil, f.getErr
}

func testObjectsCfg() *config.Config {
	return &config.Config{MinioBucket: "whatsapp-ardi", MinioShareDuration: 168 * time.Hour}
}

// TestPutWritesToTheConfiguredBucketWithTheContentType proves Put lands on
// cfg.MinioBucket (not some other bucket) and carries the caller's
// content-type through to PutObjectOptions — nothing in EnsureBuckets
// exercised either of those before this.
func TestPutWritesToTheConfiguredBucketWithTheContentType(t *testing.T) {
	f := &fakeObjectAPI{}
	objs := NewObjects(f, testObjectsCfg())

	body := []byte("hello media")
	if err := objs.Put(context.Background(), "media/sess-1/2026/08/msg-1.jpg",
		bytes.NewReader(body), int64(len(body)), "image/jpeg"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if f.putBucket != "whatsapp-ardi" {
		t.Errorf("bucket = %q, want %q", f.putBucket, "whatsapp-ardi")
	}
	if f.putKey != "media/sess-1/2026/08/msg-1.jpg" {
		t.Errorf("key = %q, want the key Put was called with", f.putKey)
	}
	if f.putContentType != "image/jpeg" {
		t.Errorf("contentType = %q, want %q", f.putContentType, "image/jpeg")
	}
	if f.putSize != int64(len(body)) {
		t.Errorf("size = %d, want %d", f.putSize, len(body))
	}
	if !bytes.Equal(f.putBody, body) {
		t.Errorf("body = %q, want %q", f.putBody, body)
	}
}

func TestPutPropagatesAnUnderlyingError(t *testing.T) {
	wantErr := errors.New("connection refused")
	f := &fakeObjectAPI{putErr: wantErr}
	objs := NewObjects(f, testObjectsCfg())

	err := objs.Put(context.Background(), "k", bytes.NewReader(nil), 0, "text/plain")
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Put err = %v, want wrapped %v", err, wantErr)
	}
}

// TestPresignedGetUsesTheConfiguredShareDuration is the one that is RED
// before this phase: cfg.MinioShareDuration was parsed by config.loadMinio
// and then read by nothing anywhere in the codebase (.env.example even said
// so). This proves PresignedGet actually threads it through to
// PresignedGetObject's expires argument, rather than some hardcoded or
// zero-value duration.
func TestPresignedGetUsesTheConfiguredShareDuration(t *testing.T) {
	f := &fakeObjectAPI{}
	cfg := testObjectsCfg()
	cfg.MinioShareDuration = 42 * time.Minute
	objs := NewObjects(f, cfg)

	url, err := objs.PresignedGet(context.Background(), "media/sess-1/2026/08/msg-1.jpg")
	if err != nil {
		t.Fatalf("PresignedGet: %v", err)
	}
	if f.presignExpires != 42*time.Minute {
		t.Fatalf("expires = %v, want the configured MinioShareDuration (42m)", f.presignExpires)
	}
	if f.presignBucket != "whatsapp-ardi" {
		t.Errorf("bucket = %q, want %q", f.presignBucket, "whatsapp-ardi")
	}
	if f.presignKey != "media/sess-1/2026/08/msg-1.jpg" {
		t.Errorf("key = %q, want the key PresignedGet was called with", f.presignKey)
	}
	if url == "" {
		t.Error("PresignedGet returned an empty URL")
	}
}

func TestPresignedGetPropagatesAnUnderlyingError(t *testing.T) {
	wantErr := errors.New("access denied")
	f := &fakeObjectAPI{presignErr: wantErr}
	objs := NewObjects(f, testObjectsCfg())

	_, err := objs.PresignedGet(context.Background(), "k")
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("PresignedGet err = %v, want wrapped %v", err, wantErr)
	}
}

// TestObjectKeyLayoutMatchesTheDocumentedShape pins the exact
// media/{session_id}/{yyyy}/{mm}/{message_id}{ext} shape this package's
// callers (Phase 6's handler) will build wa_messages.media_object_key from
// — a stored, not re-derived, value (see ObjectKey's doc comment), so
// drifting this format silently would only surface as newly-written keys
// looking different from old ones, never as a test failure without this.
func TestObjectKeyLayoutMatchesTheDocumentedShape(t *testing.T) {
	at := time.Date(2026, time.March, 5, 10, 0, 0, 0, time.UTC)
	got := ObjectKey("sess-1", "msg-42", at, ".jpg")
	want := "media/sess-1/2026/03/msg-42.jpg"
	if got != want {
		t.Fatalf("ObjectKey = %q, want %q", got, want)
	}
}
