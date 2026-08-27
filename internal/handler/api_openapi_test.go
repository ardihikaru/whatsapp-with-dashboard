package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	yaml "go.yaml.in/yaml/v3"
)

// routesFromRoutesAPI parses routes_api.go with go/ast — the same
// technique whatsmeow_import_test.go uses on import declarations, applied
// here to mux.Handle/mux.HandleFunc call arguments instead — and returns
// every registered "METHOD /path" pattern whose path starts with
// "/api/v1" (excluding the bare "GET /openapi.yaml" route, which describes
// the spec rather than being part of it).
//
// `go test` runs a package with its own directory as the working
// directory (same as whatsmeow_import_test.go's own note), so "." here is
// internal/handler.
func routesFromRoutesAPI(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "routes_api.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		// Go 1.22 mux patterns here are always "METHOD /path" — split off
		// the method, which this test does not need.
		parts := strings.SplitN(pattern, " ", 2)
		if len(parts) != 2 {
			return true
		}
		path := parts[1]
		if strings.HasPrefix(path, "/api/v1") {
			paths = append(paths, path)
		}
		return true
	})
	return paths
}

// documentedOpenAPIPaths reads the embedded openapi.yaml and returns the
// set of keys under top-level `paths:`. Go 1.22's wildcard syntax
// ("{id}", "{webhookID}", …) is IDENTICAL to OpenAPI's own path-template
// syntax, so a route pattern's path needs no rewriting to compare against
// a spec key — the "translation" is the identity function, which is why
// this looks up paths directly rather than through any rewrite step.
func documentedOpenAPIPaths(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := wsm.FS.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.yaml did not parse as YAML: %v", err)
	}
	out := make(map[string]bool, len(doc.Paths))
	for p := range doc.Paths {
		out[p] = true
	}
	return out
}

// TestEveryAPIRouteIsDocumentedInTheOpenAPISpec is the regression test the
// phase brief asks for: nothing today ties a route registered in
// routes_api.go to openapi.yaml, so a future route is complete only by
// discipline without this — it fails the moment one is added and the spec
// is not.
func TestEveryAPIRouteIsDocumentedInTheOpenAPISpec(t *testing.T) {
	routes := routesFromRoutesAPI(t)
	if len(routes) == 0 {
		t.Fatal("routesFromRoutesAPI found no /api/v1 routes at all — the AST scan itself is broken")
	}
	documented := documentedOpenAPIPaths(t)

	var missing []string
	for _, path := range routes {
		if !documented[path] {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d route(s) registered in routes_api.go have no matching entry under openapi.yaml's paths: %v", len(missing), missing)
	}
}
