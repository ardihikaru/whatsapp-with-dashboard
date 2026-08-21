package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

// newAPIClientsTestServer is the shared SQLite-backed server, named for what
// these tests exercise.
func newAPIClientsTestServer(t *testing.T) (http.Handler, *auth.Store, *message.SQLRepository, *apiclient.Repository) {
	return newLogsTestServer(t)
}

func seedClients(t *testing.T, repo *apiclient.Repository, n int) {
	t.Helper()
	ctx := context.Background()
	ttl := 86400
	for i := range n {
		name := fmt.Sprintf("Konsumen %02d", i)
		if i == 0 {
			name = "Kasir Kemang"
		}
		_, err := repo.Create(ctx, name, fmt.Sprintf("login-%02d", i), "",
			apiclient.TokenSettings{TTLSeconds: &ttl})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// The admin table must page rather than dump every client: 25 rows at a page
// size of 20 means page 1 shows 20 and page 2 exists.
func TestAPIClientsPaginates(t *testing.T) {
	srv, sessions, _, clients := newAPIClientsTestServer(t)
	seedClients(t, clients, 25)

	res := authedGet(t, srv, sessions, "/admin/api-clients")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /admin/api-clients = %d: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if got := strings.Count(body, `href="/admin/api-clients/`); got == 0 {
		t.Fatal("no client rows rendered")
	}
	if !strings.Contains(body, "Menampilkan 1–20 dari 25") {
		t.Fatalf("footer missing the row range/total: %s", body)
	}
	if !strings.Contains(body, "page=2") {
		t.Fatal("pager has no link to page 2")
	}

	p2 := authedGet(t, srv, sessions, "/admin/api-clients?page=2")
	if !strings.Contains(p2.Body.String(), "Menampilkan 21–25 dari 25") {
		t.Fatal("page 2 footer wrong")
	}
}

// Search narrows the list and survives into the pager links.
func TestAPIClientsSearch(t *testing.T) {
	srv, sessions, _, clients := newAPIClientsTestServer(t)
	seedClients(t, clients, 25)

	res := authedGet(t, srv, sessions, "/admin/api-clients?q=Kemang")
	body := res.Body.String()
	if !strings.Contains(body, "Kasir Kemang") {
		t.Fatal("search dropped the matching client")
	}
	if strings.Contains(body, "Konsumen 05") {
		t.Fatal("search did not narrow the list")
	}
	if !strings.Contains(body, "Menampilkan 1–1 dari 1") {
		t.Fatalf("search total not reflected in the footer: %s", body)
	}
}

// An out-of-range ?page must clamp to the last page, not render an empty table
// with a footer claiming rows exist.
func TestAPIClientsPageClamps(t *testing.T) {
	srv, sessions, _, clients := newAPIClientsTestServer(t)
	seedClients(t, clients, 5)

	res := authedGet(t, srv, sessions, "/admin/api-clients?page=99")
	if !strings.Contains(res.Body.String(), "Menampilkan 1–5 dari 5") {
		t.Fatalf("page=99 did not clamp: %s", res.Body.String())
	}
}

// The custom-duration input is inert unless "custom" is selected; it must ship
// disabled so the value cannot be typed and then silently discarded.
func TestAPIClientsCustomDurationDisabledByDefault(t *testing.T) {
	srv, sessions, _, _ := newAPIClientsTestServer(t)
	res := authedGet(t, srv, sessions, "/admin/api-clients")
	if !strings.Contains(res.Body.String(), "data-expiry-custom disabled") {
		t.Fatal("custom duration field is not disabled on the default (1 hari) choice")
	}
}

// Destructive row actions must confirm before firing.
func TestAPIClientsDestructiveActionsConfirm(t *testing.T) {
	srv, sessions, _, clients := newAPIClientsTestServer(t)
	seedClients(t, clients, 1)
	body := authedGet(t, srv, sessions, "/admin/api-clients").Body.String()
	if !strings.Contains(body, "Hapus Kasir Kemang secara permanen?") {
		t.Fatal("delete has no confirmation")
	}
}

// The sidebar carries the way back to the platform backoffice, absolute because
// WSM is a separate host in production.
func TestSidebarPlatformLink(t *testing.T) {
	srv, sessions, _, _ := newAPIClientsTestServer(t)
	body := authedGet(t, srv, sessions, "/admin/api-clients").Body.String()
	if !strings.Contains(body, `href="https://clino.test/platform/"`) {
		t.Fatal("sidebar is missing the back-to-platform link")
	}
	if !strings.Contains(body, "Kembali ke Platform") {
		t.Fatal("back-to-platform link has no label")
	}
}
