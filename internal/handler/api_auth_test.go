package handler

import (
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
)

func newAuthTestServer(t *testing.T) (http.Handler, *auth.Store, *apiclient.Repository) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStore(time.Hour)
	t.Cleanup(sessions.Close)
	apiClients := apiclient.NewRepository(pool)
	return New(Deps{
		Renderer: rend, Sessions: sessions,
		Middleware: auth.NewMiddleware(sessions, apiClients, ""),
		APIClients: apiClients, SessionTTL: time.Hour, AssetsFS: wsm.FS,
	}), sessions, apiClients
}

func TestAPIRejectsWebSessionBearerAndInvalidToken(t *testing.T) {
	srv, sessions, _ := newAuthTestServer(t)
	webSess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{webSess.Token, "static-token-like-value"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		srv.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("token %q got status %d, want 401", token, res.Code)
		}
	}
}

func TestAPILoginReturnsUsableAPIClientToken(t *testing.T) {
	srv, _, repo := newAuthTestServer(t)
	ttl := 86400
	created, err := repo.Create(context.Background(), "Task Manager", "taskmgr", "",
		apiclient.TokenSettings{TTLSeconds: &ttl})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"login_id":"taskmgr","secret":"` + created.Secret + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("login got status %d body %s", res.Code, res.Body.String())
	}
}
