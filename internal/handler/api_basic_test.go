package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
)

// basicFixture wraps a dummy 200 handler in RequireAPI.
//
// It deliberately does NOT go through the real route table: every endpoint
// behind RequireAPI needs a wa.Manager or a message repository, and standing
// those up would only add ways for this test to fail for reasons that have
// nothing to do with authentication. What is under test is the credential
// decision, so 200 means "admitted" and 401 means "rejected", full stop.
func basicFixture(t *testing.T, throttle *auth.Throttle) (http.Handler, *apiclient.Repository) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStore(time.Hour)
	t.Cleanup(sessions.Close)
	clients := apiclient.NewRepository(pool)
	mw := auth.NewMiddleware(sessions, clients, "").WithBasicThrottle(throttle)
	return mw.RequireAPI(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})), clients
}

func newBasicClient(t *testing.T, clients *apiclient.Repository, loginID string) string {
	t.Helper()
	created, err := clients.Create(context.Background(), "Basic", loginID, "",
		apiclient.TokenSettings{NoExpiry: true})
	if err != nil {
		t.Fatal(err)
	}
	return created.Secret
}

func doBasic(h http.Handler, loginID, secret, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req.SetBasicAuth(loginID, secret)
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPIAcceptsBasicAuth(t *testing.T) {
	h, clients := basicFixture(t, nil)
	secret := newBasicClient(t, clients, "basicuser")

	if code := doBasic(h, "basicuser", secret, "").Code; code != http.StatusOK {
		t.Fatalf("correct Basic credentials got %d, want 200", code)
	}

	for _, tc := range []struct{ name, login, secret string }{
		{"wrong secret", "basicuser", "nope"},
		{"unknown login id", "ghost", secret},
		{"empty secret", "basicuser", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doBasic(h, tc.login, tc.secret, "")
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("got %d, want 401", rec.Code)
			}
			// Without this header a Basic client cannot know which scheme to retry.
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 carries no WWW-Authenticate header")
			}
		})
	}
}

// Basic is an ALTERNATIVE, not a replacement: the token flow must still work,
// and both must resolve to the same client.
func TestBearerStillWorksAlongsideBasic(t *testing.T) {
	h, clients := basicFixture(t, nil)
	secret := newBasicClient(t, clients, "bothuser")

	issued, err := clients.Login(context.Background(), "bothuser", secret)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer got %d, want 200", rec.Code)
	}
	if code := doBasic(h, "bothuser", secret, "").Code; code != http.StatusOK {
		t.Fatalf("basic got %d, want 200", code)
	}
}

// The browser admin credential must not become an API credential through the
// Basic door — the two systems stay non-interchangeable.
func TestBasicRejectsTheBrowserAdminCredential(t *testing.T) {
	h, _ := basicFixture(t, nil)
	if code := doBasic(h, "admin", "whatever-the-operator-password-is", "").Code; code != http.StatusUnauthorized {
		t.Errorf("the browser admin credential got %d on the API, want 401", code)
	}
}

// The success cache cannot absorb an attacker who varies the secret every
// request, so the throttle is what bounds the bcrypt cost on that path.
func TestBasicThrottleBoundsFailedAttempts(t *testing.T) {
	h, clients := basicFixture(t, auth.NewThrottle(0.1, 3)) // 3, then one per 10s
	secret := newBasicClient(t, clients, "throttled")

	for i := range 3 {
		if code := doBasic(h, "throttled", "wrong", "10.9.9.9:1234").Code; code != http.StatusUnauthorized {
			t.Fatalf("attempt %d got %d, want 401 (still inside the burst)", i+1, code)
		}
	}
	rec := doBasic(h, "throttled", "wrong", "10.9.9.9:1234")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt 4 got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 carries no Retry-After header")
	}
	// A different caller must not be locked out by someone else's failures.
	if code := doBasic(h, "throttled", secret, "10.9.9.10:1234").Code; code != http.StatusOK {
		t.Errorf("a second IP got %d, want 200; the throttle must be per key", code)
	}
}

// A working caller must never throttle itself, however chatty it is.
func TestBasicSuccessesDoNotConsumeTheThrottle(t *testing.T) {
	h, clients := basicFixture(t, auth.NewThrottle(0.1, 3))
	secret := newBasicClient(t, clients, "chatty")

	for i := range 20 {
		if code := doBasic(h, "chatty", secret, "10.9.9.11:1234").Code; code != http.StatusOK {
			t.Fatalf("request %d got %d, want 200; successes must not consume the budget", i+1, code)
		}
	}
}
