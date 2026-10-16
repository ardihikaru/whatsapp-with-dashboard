package registry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
)

func mustCompileToolName() *regexp.Regexp {
	return regexp.MustCompile(`^(wsm|waxum)_[a-z0-9_]+$`)
}

// testRegistry builds a Registry against a Pool that is never dialled (no
// test here calls Run) — it exists purely so New() can be built the same
// way production does.
func testRegistry(t *testing.T) *Registry {
	t.Helper()
	pool, err := upstream.New(upstream.Config{HostsSpec: "x=http://127.0.0.1:1|http://127.0.0.1:2", WSMUser: "u", WSMPass: "p"})
	if err != nil {
		t.Fatal(err)
	}
	return New(pool)
}

// routesFromRoutesAPI parses internal/handler/routes_api.go with go/ast —
// the same technique internal/handler/api_openapi_test.go's own
// routesFromRoutesAPI uses — and returns every "METHOD /api/v1/..."
// pattern it registers, methods included (unlike that test, which only
// needs the path).
func routesFromRoutesAPI(t *testing.T) []string {
	t.Helper()
	// go test's working directory is this package's own directory
	// (internal/mcp/registry); routes_api.go lives three levels up and one
	// over, under internal/handler.
	path := filepath.Join("..", "..", "..", "internal", "handler", "routes_api.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
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
		if strings.Contains(pattern, "/api/v1") || strings.Contains(pattern, "openapi.yaml") {
			routes = append(routes, pattern)
		}
		return true
	})
	return routes
}

// exemptRoutes are /api/v1 routes that deliberately have no wsm_* tool:
//
//   - POST /api/v1/auth/login, POST /api/v1/auth/logout: this MCP
//     authenticates every call with HTTP Basic directly (api_clients are
//     mirrored across hosts for exactly this reason, per the plan); it
//     never needs a bearer token, so wrapping the endpoint that mints one
//     would be dead code.
//   - GET /openapi.yaml: describes the spec, isn't part of the API surface
//     a tool would call.
var exemptRoutes = map[string]bool{
	"POST /api/v1/auth/login":  true,
	"POST /api/v1/auth/logout": true,
	"GET /openapi.yaml":        true,
}

// TestEveryAPIRouteHasAnMCPTool is the sync-rule test CLAUDE.md's "wsm-mcp
// must track every WSM and waxum change" section requires: a route added
// to routes_api.go with no matching tool fails this, not silently.
func TestEveryAPIRouteHasAnMCPTool(t *testing.T) {
	routes := routesFromRoutesAPI(t)
	if len(routes) == 0 {
		t.Fatal("routesFromRoutesAPI found no routes at all — the AST scan itself is broken")
	}
	covered := map[string]bool{}
	for _, tool := range testRegistry(t).List() {
		if tool.Route != "" {
			covered[tool.Route] = true
		}
	}
	var missing []string
	for _, r := range routes {
		if exemptRoutes[r] {
			continue
		}
		if !covered[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("%d route(s) in routes_api.go have no MCP tool (no Tool.Route match): %v", len(missing), missing)
	}
}

// exemptWaxumMethods are exported *waxum.Client methods with no matching
// waxum_* tool. Currently empty: every exported method, including
// TailEvents, is wrapped (TailEvents by waxum_events_tail, bounded by
// max_events/seconds since its signature streams via callback rather than
// returning a value).
var exemptWaxumMethods = map[string]bool{}

// waxumClientMethods parses every internal/waxum/*.go file (excluding
// _test.go) and returns the exported method names declared on *Client.
func waxumClientMethods(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "internal", "waxum")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recvType := fn.Recv.List[0].Type
			star, ok := recvType.(*ast.StarExpr)
			if !ok {
				continue
			}
			ident, ok := star.X.(*ast.Ident)
			if !ok || ident.Name != "Client" {
				continue
			}
			if ast.IsExported(fn.Name.Name) {
				methods = append(methods, fn.Name.Name)
			}
		}
	}
	return methods
}

// TestEveryWaxumClientMethodHasAnMCPTool is the waxum half of the sync
// rule: an exported method added to *waxum.Client with no matching
// waxum_* tool fails this.
func TestEveryWaxumClientMethodHasAnMCPTool(t *testing.T) {
	methods := waxumClientMethods(t)
	if len(methods) == 0 {
		t.Fatal("waxumClientMethods found no exported methods at all — the AST scan itself is broken")
	}
	covered := map[string]bool{}
	for _, tool := range testRegistry(t).List() {
		if tool.WaxumMethod != "" {
			covered[tool.WaxumMethod] = true
		}
	}
	var missing []string
	for _, m := range methods {
		if exemptWaxumMethods[m] {
			continue
		}
		if !covered[m] {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("%d exported *waxum.Client method(s) have no MCP tool (no Tool.WaxumMethod match): %v", len(missing), missing)
	}
}

// TestToolNamesFollowTheHouseNamingConvention asserts the well-formedness
// rules the plan specifies: name pattern, a real description, and every
// Danger tool also being non-readonly (a destructive tool cannot honestly
// claim readOnlyHint:true).
func TestToolNamesFollowTheHouseNamingConvention(t *testing.T) {
	nameRe := mustCompileToolName()
	for _, tool := range testRegistry(t).List() {
		if !nameRe.MatchString(tool.Name) {
			t.Errorf("tool %q does not match ^(wsm|waxum)_[a-z0-9_]+$", tool.Name)
		}
		if len(tool.Desc) < 20 {
			t.Errorf("tool %q has a description under 20 chars: %q", tool.Name, tool.Desc)
		}
		if tool.Danger && tool.ReadOnly {
			t.Errorf("tool %q is both Danger and ReadOnly, which cannot both be true", tool.Name)
		}
	}
}
