package apiclient

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

func newTestRepo(t *testing.T) *Repository {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return NewRepository(pool)
}

func TestCreateLoginAuthenticateAndRevoke(t *testing.T) {
	repo := newTestRepo(t)
	ttl := 86400
	created, err := repo.Create(context.Background(), "Task Manager", "taskmgr", "integration",
		TokenSettings{TTLSeconds: &ttl})
	if err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" || created.Client.SecretHash == created.Secret {
		t.Fatal("secret must be generated and stored hashed")
	}
	issued, err := repo.Login(context.Background(), "taskmgr", created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Token == "" || issued.ExpiresAt == nil {
		t.Fatal("expected expiring token")
	}
	principal, ok := repo.AuthenticateBearer(context.Background(), issued.Token)
	if !ok || principal.ClientID != created.Client.ID || principal.LoginID != "taskmgr" {
		t.Fatalf("token did not authenticate: %#v ok=%v", principal, ok)
	}
	if err := repo.RevokeToken(context.Background(), principal.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.AuthenticateBearer(context.Background(), issued.Token); ok {
		t.Fatal("revoked token authenticated")
	}
}

func TestNoExpiryAndInactiveClient(t *testing.T) {
	repo := newTestRepo(t)
	created, err := repo.Create(context.Background(), "Forever", "forever", "",
		TokenSettings{NoExpiry: true})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := repo.Login(context.Background(), "forever", created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if issued.ExpiresAt != nil {
		t.Fatal("expected no-expiry token")
	}
	if err := repo.SetActive(context.Background(), created.Client.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Login(context.Background(), "forever", created.Secret); err != ErrInactiveClient {
		t.Fatalf("expected inactive error, got %v", err)
	}
	if _, ok := repo.AuthenticateBearer(context.Background(), issued.Token); ok {
		t.Fatal("token for inactive client authenticated")
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	repo := newTestRepo(t)
	now := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return now }
	ttl := 1
	created, err := repo.Create(context.Background(), "Short", "short", "", TokenSettings{TTLSeconds: &ttl})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := repo.Login(context.Background(), "short", created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	repo.now = func() time.Time { return now.Add(2 * time.Second) }
	if _, ok := repo.AuthenticateBearer(context.Background(), issued.Token); ok {
		t.Fatal("expired token authenticated")
	}
}

func TestRotateSecretRevokesTokens(t *testing.T) {
	repo := newTestRepo(t)
	ttl := 86400
	created, err := repo.Create(context.Background(), "Rotating", "rotate", "", TokenSettings{TTLSeconds: &ttl})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := repo.Login(context.Background(), "rotate", created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	newSecret, err := repo.RotateSecret(context.Background(), created.Client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newSecret == "" || newSecret == created.Secret {
		t.Fatal("expected new one-time secret")
	}
	if _, ok := repo.AuthenticateBearer(context.Background(), issued.Token); ok {
		t.Fatal("old token authenticated after secret rotation")
	}
	if _, err := repo.Login(context.Background(), "rotate", created.Secret); err != ErrInvalidCredentials {
		t.Fatalf("expected old secret rejection, got %v", err)
	}
	if _, err := repo.Login(context.Background(), "rotate", newSecret); err != nil {
		t.Fatal(err)
	}
}
