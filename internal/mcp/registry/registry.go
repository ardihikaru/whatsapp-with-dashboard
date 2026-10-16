// Package registry is the shared wsm-mcp tool registry: one Tool per WSM
// /api/v1 route and one per exported internal/waxum.Client method, served
// identically over MCP Streamable HTTP (internal/mcp/mcpsrv) and gRPC
// (internal/mcp/grpcsrv). Keeping this registry in sync with both surfaces
// it wraps is enforced by sync_test.go — see CLAUDE.md's "wsm-mcp must
// track every WSM and waxum change" section.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
)

// Args are decoded JSON tool arguments.
type Args map[string]any

func (a Args) Str(k string) string {
	switch v := a[k].(type) {
	case string:
		return strings.TrimSpace(v)
	}
	return ""
}

func (a Args) Bool(k string) bool {
	switch v := a[k].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

func (a Args) Int(k string, def int) int {
	switch v := a[k].(type) {
	case float64:
		return int(v)
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func (a Args) StrSlice(k string) []string {
	v, ok := a[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(v))
	for _, x := range v {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Param describes one JSON Schema input property.
type Param struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"` // string | integer | boolean | array
	Desc     string   `json:"description"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

// Tool is one capability exposed to both front doors.
type Tool struct {
	Name string
	Desc string

	// Route is the "METHOD /api/v1/..." this tool wraps, as it appears in
	// routes_api.go — empty for tools that wrap no single WSM route
	// (waxum_* tools and the two extra wsm_resolve_session/wsm_hosts_status
	// tools). sync_test.go's TestEveryAPIRouteHasAnMCPTool keys on this.
	Route string
	// WaxumMethod is the exported internal/waxum.Client method name this
	// tool wraps, e.g. "SendText" — empty for wsm_* route tools.
	// sync_test.go's TestEveryWaxumClientMethodHasAnMCPTool keys on this.
	WaxumMethod string

	Params []Param

	// Danger gates destructive/irreversible calls behind
	// WSM_MCP_ALLOW_DANGEROUS (default off) — see upstream.DangerGate.
	Danger bool
	// ReadOnly/Idempotent feed MCP tool annotations (readOnlyHint etc).
	ReadOnly   bool
	Idempotent bool

	Run func(ctx context.Context, p *upstream.Pool, a Args) (any, error)
}

// InputSchema renders Params as a JSON Schema object, plus the two
// resolver args ("session_id"/"phone") and "target" every session-scoped
// tool also accepts — added by the tool itself via Params, not injected
// here, so the schema stays an honest description of what Run reads.
func (t Tool) InputSchema() map[string]any {
	props, req := map[string]any{}, []string{}
	for _, p := range t.Params {
		s := map[string]any{"type": p.Type, "description": p.Desc}
		if len(p.Enum) > 0 {
			s["enum"] = p.Enum
		}
		props[p.Name] = s
		if p.Required {
			req = append(req, p.Name)
		}
	}
	out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(req) > 0 {
		out["required"] = req
	}
	return out
}

// Annotations renders the MCP tool annotation object.
func (t Tool) Annotations() map[string]any {
	return map[string]any{
		"readOnlyHint":    t.ReadOnly,
		"destructiveHint": !t.ReadOnly,
		"idempotentHint":  t.Idempotent,
		"openWorldHint":   true,
	}
}

// Registry holds every tool, keyed by name.
type Registry struct {
	byName map[string]Tool
	order  []string
	pool   *upstream.Pool
}

// New builds the registry from every tool group plus the pool each Run
// closure dials through.
func New(pool *upstream.Pool) *Registry {
	r := &Registry{byName: map[string]Tool{}, pool: pool}
	groups := [][]Tool{wsmRouteTools(), waxumMethodTools(), extraTools()}
	for _, g := range groups {
		for _, t := range g {
			if _, dup := r.byName[t.Name]; dup {
				panic("duplicate mcp tool name " + t.Name)
			}
			r.byName[t.Name] = t
			r.order = append(r.order, t.Name)
		}
	}
	sort.Strings(r.order)
	return r
}

// List returns every tool, name-ordered.
func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.byName[n])
	}
	return out
}

// Has reports whether name is registered.
func (r *Registry) Has(name string) bool { _, ok := r.byName[name]; return ok }

// Call runs one tool with raw JSON arguments. Danger-gated tools refuse
// before Run is ever called when WSM_MCP_ALLOW_DANGEROUS is not set.
func (r *Registry) Call(ctx context.Context, name string, raw []byte) (any, error) {
	t, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	a := Args{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	for _, p := range t.Params {
		if _, set := a[p.Name]; p.Required && !set {
			return nil, fmt.Errorf("missing required argument %q", p.Name)
		}
	}
	if t.Danger && !r.pool.AllowDangerous {
		return nil, fmt.Errorf("tool %q is disabled: set WSM_MCP_ALLOW_DANGEROUS=true to enable destructive tools", name)
	}
	return t.Run(ctx, r.pool, a)
}
