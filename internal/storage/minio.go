// Package storage wires up the MinIO object-storage connection (New,
// EnsureBuckets — established and verified at boot, per the "required at
// boot" decision in config) and the object-level API handlers use to reach
// it (objects.go's ObjectStore / MinioObjects).
package storage

import (
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/config"
)

// New builds a MinIO client from the resolved config. It does not touch the
// network — minio.New only validates its arguments locally.
func New(cfg *config.Config) (*minio.Client, error) {
	return minio.New(cfg.MinioHost, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.MinioAccessKey, cfg.MinioSecretKey, ""),
		Secure: cfg.MinioSecure,
	})
}

// bucketAPI is the slice of *minio.Client this package needs, kept as an
// interface so EnsureBuckets is testable without a live MinIO server.
type bucketAPI interface {
	BucketExists(ctx context.Context, bucketName string) (bool, error)
	MakeBucket(ctx context.Context, bucketName string, opts minio.MakeBucketOptions) error
}

// EnsureBuckets makes sure cfg.MinioBucket and cfg.MinioTempBucket exist,
// creating whichever is missing. Mirrors internal/db.EnsureSchema: ensure the
// resource is there rather than fail because a fresh environment hasn't
// created it yet.
func EnsureBuckets(ctx context.Context, client bucketAPI, cfg *config.Config) error {
	for _, bucket := range []string{cfg.MinioBucket, cfg.MinioTempBucket} {
		exists, err := client.BucketExists(ctx, bucket)
		if err != nil {
			return fmt.Errorf("checking MinIO bucket %q: %w", bucket, err)
		}
		if exists {
			continue
		}
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("creating MinIO bucket %q: %w", bucket, err)
		}
	}
	return nil
}
