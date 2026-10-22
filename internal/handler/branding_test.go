package handler

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
)

// TestTheSidebarAndPageTitleUseWSMBrandingNotClino is the test that goes red
// the moment sidebar.html or base.html reverts to the old "Clino" wording —
// this standalone app has no "Clino platform" to refer back to (see
// CLAUDE.md's PlatformURL doc comment: upstream pointed at a platform
// backoffice that does not exist here).
func TestTheSidebarAndPageTitleUseWSMBrandingNotClino(t *testing.T) {
	// newHostLabelTestServer (host_chip_test.go), not newLogsTestServer: the
	// latter sets PlatformURL to a "clino.test" fixture host that would make
	// this test fail for an unrelated, out-of-scope reason (test fixture
	// data, not UI branding — see CLAUDE.md's "out of scope" list).
	srv, sessions := newHostLabelTestServer(t, "")
	body := authedGet(t, srv, sessions, "/dashboard").Body.String()

	if !strings.Contains(body, "WSM&nbsp;· WhatsApp") {
		t.Errorf("sidebar is missing the WSM brand span; got:\n%s", body)
	}
	if !strings.Contains(body, "· WSM WhatsApp</title>") {
		t.Errorf("page title is missing \"· WSM WhatsApp\"; got:\n%s", body)
	}
	if strings.Contains(strings.ToLower(body), "clino") {
		t.Errorf("rendered dashboard page still mentions \"clino\"; got:\n%s", body)
	}
}

// TestTheLoginPageTitleUsesWSMBranding is login.html's half of the above —
// that template duplicates base.html's whole <head> (CLAUDE.md's hazard
// entry on this) rather than including it, so it can drift independently.
func TestTheLoginPageTitleUsesWSMBranding(t *testing.T) {
	srv, _ := loginMux(t, false)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/login", nil))
	body := res.Body.String()

	if !strings.Contains(body, "· WSM WhatsApp</title>") && !strings.Contains(body, "Sign in · WSM</title>") {
		t.Errorf("login page title does not look WSM-branded; got:\n%s", body)
	}
	if strings.Contains(strings.ToLower(body), "clino") {
		t.Errorf("rendered login page still mentions \"clino\"; got:\n%s", body)
	}
}

// TestNoEmbeddedTemplateOrCSSFileMentionsClino walks every file actually
// shipped in the binary (assets.go's wsm.FS) and fails if any one of them
// contains the word "clino", case-insensitively — the standalone rename's
// backstop against a stray reference anywhere in web/templates or
// web/static/css, not just the handful of spots found by a one-time grep.
//
// Deliberately scoped to templates+CSS, not the whole embedded tree: it
// must never start asserting on web/static/js or openapi.yaml, which this
// task never touched and which could legitimately carry the word for
// reasons unrelated to UI branding.
func TestNoEmbeddedTemplateOrCSSFileMentionsClino(t *testing.T) {
	roots := []string{"web/templates", "web/static/css"}
	for _, root := range roots {
		err := fs.WalkDir(wsm.FS, root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(wsm.FS, path)
			if err != nil {
				return err
			}
			if strings.Contains(strings.ToLower(string(b)), "clino") {
				t.Errorf("%s mentions \"clino\"", path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
