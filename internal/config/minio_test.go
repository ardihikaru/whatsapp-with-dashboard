package config

import (
	"testing"
	"time"
)

func setMinioEnv(t *testing.T) {
	t.Helper()
	t.Setenv("WSM_MINIO_ENDPOINT", "https://s3.mfardiansyah.id")
	t.Setenv("WSM_MINIO_ACCESS_KEY", "access-key")
	t.Setenv("WSM_MINIO_SECRET_KEY", "secret-key")
	t.Setenv("WSM_MINIO_BUCKET", "whatsapp-ardi")
}

func TestLoadMinioParsesAFullValidEnvironment(t *testing.T) {
	setMinioEnv(t)
	t.Setenv("WSM_MINIO_TEMP_BUCKET", "temp")
	t.Setenv("WSM_MINIO_TIMEOUT", "100s")
	t.Setenv("WSM_MINIO_SHARE_DURATION", "168h")

	cfg := &Config{}
	if err := loadMinio(cfg); err != nil {
		t.Fatalf("loadMinio: %v", err)
	}
	if cfg.MinioHost != "s3.mfardiansyah.id" {
		t.Errorf("MinioHost = %q, want %q", cfg.MinioHost, "s3.mfardiansyah.id")
	}
	if !cfg.MinioSecure {
		t.Error("MinioSecure = false, want true for an https endpoint")
	}
	if cfg.MinioBucket != "whatsapp-ardi" {
		t.Errorf("MinioBucket = %q, want %q", cfg.MinioBucket, "whatsapp-ardi")
	}
	if cfg.MinioTempBucket != "temp" {
		t.Errorf("MinioTempBucket = %q, want %q", cfg.MinioTempBucket, "temp")
	}
	if cfg.MinioTimeout != 100*time.Second {
		t.Errorf("MinioTimeout = %v, want 100s", cfg.MinioTimeout)
	}
	if cfg.MinioShareDuration != 168*time.Hour {
		t.Errorf("MinioShareDuration = %v, want 168h", cfg.MinioShareDuration)
	}
}

func TestLoadMinioDefaultsTempBucketTimeoutAndShareDuration(t *testing.T) {
	setMinioEnv(t)
	cfg := &Config{}
	if err := loadMinio(cfg); err != nil {
		t.Fatalf("loadMinio: %v", err)
	}
	if cfg.MinioTempBucket != DefaultMinioTempBucket {
		t.Errorf("MinioTempBucket = %q, want default %q", cfg.MinioTempBucket, DefaultMinioTempBucket)
	}
	if cfg.MinioTimeout != DefaultMinioTimeout {
		t.Errorf("MinioTimeout = %v, want default %v", cfg.MinioTimeout, DefaultMinioTimeout)
	}
	if cfg.MinioShareDuration != DefaultMinioShareDuration {
		t.Errorf("MinioShareDuration = %v, want default %v", cfg.MinioShareDuration, DefaultMinioShareDuration)
	}
}

// Each of these is load-bearing on its own: dropping any one of them must
// fail the boot rather than silently run against no object storage.
func TestLoadMinioFailsWhenARequiredVarIsUnset(t *testing.T) {
	required := []string{
		"WSM_MINIO_ENDPOINT",
		"WSM_MINIO_ACCESS_KEY",
		"WSM_MINIO_SECRET_KEY",
		"WSM_MINIO_BUCKET",
	}
	for _, missing := range required {
		t.Run(missing, func(t *testing.T) {
			setMinioEnv(t)
			t.Setenv(missing, "")
			if err := loadMinio(&Config{}); err == nil {
				t.Fatalf("loadMinio with %s unset: got nil error, want a failure", missing)
			}
		})
	}
}

func TestLoadMinioRejectsAMalformedEndpoint(t *testing.T) {
	cases := []string{
		"not-a-url",
		"ftp://s3.mfardiansyah.id",
		"https://",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			setMinioEnv(t)
			t.Setenv("WSM_MINIO_ENDPOINT", raw)
			if err := loadMinio(&Config{}); err == nil {
				t.Fatalf("loadMinio(%q): got nil error, want a failure", raw)
			}
		})
	}
}

func TestLoadMinioRejectsAnInvalidDuration(t *testing.T) {
	for _, env := range []string{"WSM_MINIO_TIMEOUT", "WSM_MINIO_SHARE_DURATION"} {
		t.Run(env, func(t *testing.T) {
			setMinioEnv(t)
			t.Setenv(env, "not-a-duration")
			if err := loadMinio(&Config{}); err == nil {
				t.Fatalf("loadMinio with %s=not-a-duration: got nil error, want a failure", env)
			}
		})
	}
}
