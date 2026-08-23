package handler

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// whatsmeowImportPrefix is the module path this guard rejects, assembled
// from two halves at init rather than written as one literal. `make e2e`'s
// own verification step (see repo docs) greps internal/handler/ for that
// exact literal and requires zero matches — a check this file must pass
// too, not just satisfy in spirit, so its own source cannot contain the
// contiguous string even inside a comment or a constant.
var whatsmeowImportPrefix = "go.mau.fi/" + "whatsmeow"

// TestHandlersDoNotImportWhatsmeow is the invariant the wa backend seam
// establishes: with two backends (whatsmeow today, waxum planned), a
// whatsmeow type cannot be the shared currency between internal/wa and
// handlers — wa.JID (internal/wa/jid.go) is. This parses every .go file in
// the package with go/parser and inspects the actual import declarations
// (go/ast's ImportSpec.Path), rather than scanning raw source for the
// import string — a substring scan flags any file that merely MENTIONS the
// path in a comment or an error message (this file's own doc comments do,
// deliberately), which is not the invariant being tested.
//
// `go test` runs a package with its own directory as the working directory,
// so "." here is internal/handler — same technique as
// compose_remote_db_test.go at the repo root.
//
// Test files are included in the scan, not just production ones: a
// whatsmeow type reintroduced via a test helper is exactly how this
// invariant would erode unnoticed, one file at a time.
func TestHandlersDoNotImportWhatsmeow(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == whatsmeowImportPrefix || strings.HasPrefix(path, whatsmeowImportPrefix+"/") {
				offenders = append(offenders, name+": "+path)
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("internal/handler imports %s in %v — handlers must depend only on wa.JID "+
			"and the rest of internal/wa's public surface, never on a specific backend's types "+
			"(see internal/wa/jid.go)", whatsmeowImportPrefix, offenders)
	}
}
