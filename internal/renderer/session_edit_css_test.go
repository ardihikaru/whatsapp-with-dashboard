package renderer_test

import (
	"io/fs"
	"strings"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
)

// TestEveryHeadTemplateLinksTheSessionEditStylesheet is
// responsive_test.go's TestEveryHeadTemplateLinksTheResponsiveStylesheet
// for session-edit.css (Part 3's dialog/chip/kebab-menu styles) —
// login.html duplicates base.html's entire <head> rather than sharing it
// (CLAUDE.md hazard), so a new stylesheet has to be added in both files by
// hand and nothing before this test compared them for THIS file.
func TestEveryHeadTemplateLinksTheSessionEditStylesheet(t *testing.T) {
	found := map[string]string{}
	err := fs.WalkDir(wsm.FS, "web/templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := fs.ReadFile(wsm.FS, path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "<head>") {
			found[path] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	if len(found) < 2 {
		t.Fatalf("expected at least base.html and login.html to own a <head>, found %d", len(found))
	}
	for path, src := range found {
		t.Run(path, func(t *testing.T) {
			if !strings.Contains(src, "/static/css/session-edit.css") {
				t.Errorf("%s owns a <head> but does not link session-edit.css", path)
			}
		})
	}
}

// TestSessionEditAssetsExistInTheEmbeddedFS mirrors
// TestResponsiveAssetsExistInTheEmbeddedFS: a stylesheet/script named in a
// <link>/<script> but absent from the embedded FS is a 404 in the browser
// and a green test suite everywhere else.
func TestSessionEditAssetsExistInTheEmbeddedFS(t *testing.T) {
	for _, path := range []string{
		"web/static/css/session-edit.css",
		"web/static/js/session-edit.js",
	} {
		if _, err := fs.ReadFile(wsm.FS, path); err != nil {
			t.Errorf("%s is linked by a template but missing from the embedded FS: %v", path, err)
		}
	}
}
