package apiclient

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A date-based client stamps the ABSOLUTE cutoff on the token. The distinguishing
// case is a cutoff that is nearer than the default 24h TTL would be: if the code
// fell back to now+TTL the token would outlive the date the admin set.
func TestDateExpiryStampsTheCutoffNotATTL(t *testing.T) {
	repo := newTestRepo(t)
	cutoff := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	created, err := repo.Create(context.Background(), "Sampai Tanggal", "sampai", "",
		TokenSettings{ExpiresAt: &cutoff})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := repo.Login(context.Background(), "sampai", created.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if issued.ExpiresAt == nil {
		t.Fatal("date-based client issued a non-expiring token")
	}
	if !issued.ExpiresAt.Equal(cutoff) {
		t.Fatalf("token expires at %s, want the client cutoff %s", issued.ExpiresAt, cutoff)
	}
}

// Past the cutoff, login itself must fail. Handing back an already-dead token
// would read as a successful login and 401 on the very next call.
func TestLoginRefusedAfterTheCutoff(t *testing.T) {
	repo := newTestRepo(t)
	cutoff := time.Now().UTC().Add(-time.Minute)
	created, err := repo.Create(context.Background(), "Sudah Lewat", "lewat", "",
		TokenSettings{ExpiresAt: &cutoff})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Login(context.Background(), "lewat", created.Secret); !errors.Is(err, ErrExpiredClient) {
		t.Fatalf("login after the cutoff returned %v, want ErrExpiredClient", err)
	}
}

// The cutoff must survive the round trip through the store, and Update must be
// able to switch a client from a duration to a date and back.
func TestExpiryModesRoundTrip(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	ttl := 3 * 365 * 86400
	created, err := repo.Create(ctx, "Tiga Tahun", "tiga", "", TokenSettings{TTLSeconds: &ttl})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Client.ID

	cutoff := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	if err := repo.Update(ctx, id, "Tiga Tahun", "tiga", "", TokenSettings{ExpiresAt: &cutoff}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenExpiresAt == nil || !got.TokenExpiresAt.Equal(cutoff) {
		t.Fatalf("cutoff did not round-trip: %v", got.TokenExpiresAt)
	}

	// Switching back to a duration must CLEAR the cutoff, not leave it winning.
	if err := repo.Update(ctx, id, "Tiga Tahun", "tiga", "", TokenSettings{TTLSeconds: &ttl}); err != nil {
		t.Fatal(err)
	}
	got, err = repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenExpiresAt != nil {
		t.Fatalf("cutoff survived a switch back to a duration: %v", got.TokenExpiresAt)
	}
	if got.TokenTTLSeconds == nil || *got.TokenTTLSeconds != ttl {
		t.Fatalf("ttl not restored: %v", got.TokenTTLSeconds)
	}
}
