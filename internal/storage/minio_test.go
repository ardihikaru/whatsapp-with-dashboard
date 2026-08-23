package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
)

// fakeBucketAPI records calls instead of talking to a network, so
// EnsureBuckets is testable without a live MinIO server.
type fakeBucketAPI struct {
	exists      map[string]bool
	existsErr   map[string]error
	makeErr     map[string]error
	madeBuckets []string
}

func (f *fakeBucketAPI) BucketExists(_ context.Context, bucketName string) (bool, error) {
	if err := f.existsErr[bucketName]; err != nil {
		return false, err
	}
	return f.exists[bucketName], nil
}

func (f *fakeBucketAPI) MakeBucket(_ context.Context, bucketName string, _ minio.MakeBucketOptions) error {
	if err := f.makeErr[bucketName]; err != nil {
		return err
	}
	f.madeBuckets = append(f.madeBuckets, bucketName)
	return nil
}

func testCfg() *config.Config {
	return &config.Config{MinioBucket: "whatsapp-ardi", MinioTempBucket: "temp"}
}

func TestEnsureBucketsSkipsCreationWhenBothBucketsAlreadyExist(t *testing.T) {
	f := &fakeBucketAPI{exists: map[string]bool{"whatsapp-ardi": true, "temp": true}}
	if err := EnsureBuckets(context.Background(), f, testCfg()); err != nil {
		t.Fatalf("EnsureBuckets: %v", err)
	}
	if len(f.madeBuckets) != 0 {
		t.Fatalf("expected no MakeBucket calls, got %v", f.madeBuckets)
	}
}

func TestEnsureBucketsCreatesWhicheverBucketIsMissing(t *testing.T) {
	f := &fakeBucketAPI{exists: map[string]bool{"whatsapp-ardi": true, "temp": false}}
	if err := EnsureBuckets(context.Background(), f, testCfg()); err != nil {
		t.Fatalf("EnsureBuckets: %v", err)
	}
	if got := f.madeBuckets; len(got) != 1 || got[0] != "temp" {
		t.Fatalf("expected MakeBucket(temp) only, got %v", got)
	}
}

func TestEnsureBucketsPropagatesBucketExistsError(t *testing.T) {
	wantErr := errors.New("network unreachable")
	f := &fakeBucketAPI{existsErr: map[string]error{"whatsapp-ardi": wantErr}}
	err := EnsureBuckets(context.Background(), f, testCfg())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
}

func TestEnsureBucketsPropagatesMakeBucketError(t *testing.T) {
	wantErr := errors.New("access denied")
	f := &fakeBucketAPI{
		exists:  map[string]bool{"whatsapp-ardi": false},
		makeErr: map[string]error{"whatsapp-ardi": wantErr},
	}
	err := EnsureBuckets(context.Background(), f, testCfg())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
}
