package apiclient

import (
	"context"
	"testing"
	"time"
)

func newBasicTestClient(t *testing.T, loginID string) (*Repository, CreatedClient) {
	t.Helper()
	repo := newTestRepo(t)
	created, err := repo.Create(context.Background(), "Basic Caller", loginID, "", TokenSettings{NoExpiry: true})
	if err != nil {
		t.Fatal(err)
	}
	return repo, created
}

func TestAuthenticateBasic(t *testing.T) {
	repo, created := newBasicTestClient(t, "basiccaller")
	ctx := context.Background()

	principal, ok := repo.AuthenticateBasic(ctx, "basiccaller", created.Secret)
	if !ok {
		t.Fatal("correct credentials were rejected")
	}
	if principal.ClientID != created.Client.ID || principal.LoginID != "basiccaller" {
		t.Fatalf("wrong principal: %#v", principal)
	}
	// Basic mints no token, so there is nothing to revoke and nothing to record
	// a last_used_at against.
	if principal.TokenHash != "" {
		t.Errorf("TokenHash = %q, want empty; Basic must not mint a token", principal.TokenHash)
	}

	for _, tc := range []struct{ name, login, secret string }{
		{"wrong secret", "basiccaller", "nope"},
		{"unknown login id", "ghost", created.Secret},
		{"empty secret", "basiccaller", ""},
		{"empty login id", "", created.Secret},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := repo.AuthenticateBasic(ctx, tc.login, tc.secret); ok {
				t.Error("bad credentials authenticated")
			}
		})
	}
}

// The cache is the whole point of the Basic path: without it every request pays
// a bcrypt compare.
func TestAuthenticateBasicUsesTheCache(t *testing.T) {
	repo, created := newBasicTestClient(t, "cached")
	ctx := context.Background()

	if _, ok := repo.AuthenticateBasic(ctx, "cached", created.Secret); !ok {
		t.Fatal("first verification failed")
	}
	if len(repo.basic.byKey) != 1 {
		t.Fatalf("cache holds %d entries after one success, want 1", len(repo.basic.byKey))
	}

	// Pull the database out from under it. A second call that still succeeds can
	// only have been served from the cache.
	repo.pool.Close()
	if _, ok := repo.AuthenticateBasic(ctx, "cached", created.Secret); !ok {
		t.Error("second verification missed the cache (it hit the closed database)")
	}
	// A WRONG secret must not be served by the cache even for a cached client.
	if _, ok := repo.AuthenticateBasic(ctx, "cached", "nope"); ok {
		t.Error("a wrong secret was admitted; the cache key must bind login id AND secret")
	}
}

func TestBasicCacheEntriesExpire(t *testing.T) {
	repo, created := newBasicTestClient(t, "expiring")
	ctx := context.Background()

	base := time.Now().UTC()
	repo.now = func() time.Time { return base }
	if _, ok := repo.AuthenticateBasic(ctx, "expiring", created.Secret); !ok {
		t.Fatal("first verification failed")
	}
	repo.pool.Close()

	// Still inside the TTL: served from cache despite the dead pool.
	repo.now = func() time.Time { return base.Add(basicCacheTTL - time.Second) }
	if _, ok := repo.AuthenticateBasic(ctx, "expiring", created.Secret); !ok {
		t.Error("entry expired early")
	}
	// Past the TTL: must fall through to the database, which is now closed.
	repo.now = func() time.Time { return base.Add(basicCacheTTL + time.Second) }
	if _, ok := repo.AuthenticateBasic(ctx, "expiring", created.Secret); ok {
		t.Error("a stale cache entry was served past its TTL")
	}
}

// Each of these mutations can make a cached answer WRONG. A cached credential
// surviving any of them is a revoked client that still gets in.
func TestBasicCacheInvalidatedByMutations(t *testing.T) {
	ctx := context.Background()

	t.Run("deactivate", func(t *testing.T) {
		repo, created := newBasicTestClient(t, "deact")
		repo.AuthenticateBasic(ctx, "deact", created.Secret)
		if err := repo.SetActive(ctx, created.Client.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, ok := repo.AuthenticateBasic(ctx, "deact", created.Secret); ok {
			t.Error("a deactivated client still authenticated")
		}
	})

	t.Run("rotate secret", func(t *testing.T) {
		repo, created := newBasicTestClient(t, "rotate")
		repo.AuthenticateBasic(ctx, "rotate", created.Secret)
		newSecret, err := repo.RotateSecret(ctx, created.Client.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := repo.AuthenticateBasic(ctx, "rotate", created.Secret); ok {
			t.Error("the replaced secret still authenticated")
		}
		if _, ok := repo.AuthenticateBasic(ctx, "rotate", newSecret); !ok {
			t.Error("the new secret did not authenticate")
		}
	})

	t.Run("delete", func(t *testing.T) {
		repo, created := newBasicTestClient(t, "gone")
		repo.AuthenticateBasic(ctx, "gone", created.Secret)
		if err := repo.Delete(ctx, created.Client.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok := repo.AuthenticateBasic(ctx, "gone", created.Secret); ok {
			t.Error("a deleted client still authenticated")
		}
	})

	t.Run("login id changed", func(t *testing.T) {
		repo, created := newBasicTestClient(t, "oldname")
		repo.AuthenticateBasic(ctx, "oldname", created.Secret)
		if err := repo.Update(ctx, created.Client.ID, "Basic Caller", "newname", "",
			TokenSettings{NoExpiry: true}); err != nil {
			t.Fatal(err)
		}
		if _, ok := repo.AuthenticateBasic(ctx, "oldname", created.Secret); ok {
			t.Error("the old login id still authenticated")
		}
		if _, ok := repo.AuthenticateBasic(ctx, "newname", created.Secret); !ok {
			t.Error("the new login id did not authenticate")
		}
	})
}

// Bearer and Basic must stay interchangeable in what they PRODUCE, so nothing
// downstream has to care which was used.
func TestBasicAndBearerYieldTheSameClient(t *testing.T) {
	repo, created := newBasicTestClient(t, "both")
	ctx := context.Background()

	issued, err := repo.Login(ctx, "both", created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	viaBearer, ok1 := repo.AuthenticateBearer(ctx, issued.Token)
	viaBasic, ok2 := repo.AuthenticateBasic(ctx, "both", created.Secret)
	if !ok1 || !ok2 {
		t.Fatal("one of the two paths failed")
	}
	if viaBearer.ClientID != viaBasic.ClientID || viaBearer.LoginID != viaBasic.LoginID {
		t.Errorf("paths disagree: bearer=%#v basic=%#v", viaBearer, viaBasic)
	}
}
