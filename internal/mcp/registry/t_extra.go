package registry

import (
	"context"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
)

// extraTools are the two tools the plan asks for beyond a 1:1 route/method
// wrapper: a phone->session_id resolver across every host, and a combined
// WSM+waxum health check across every host. Neither wraps a single route
// or waxum method, so both have empty Route/WaxumMethod and are exempt
// from sync_test.go's two coverage tests (see their own exemption
// comments).
func extraTools() []Tool {
	return []Tool{
		{
			Name: "wsm_resolve_session", ReadOnly: true, Idempotent: true,
			Desc:   "Resolve a phone number to its session_id on every configured host. The SAME phone has a DIFFERENT session_id per host, so this is the tool to call before using session_id directly with a target pin, or just to discover what exists. Defaults to WSM_MCP_DEFAULT_PHONE when phone is omitted.",
			Params: []Param{{Name: "phone", Type: "string", Desc: "Phone number (62xxxx/0xxxx/+62xxxx). Defaults to WSM_MCP_DEFAULT_PHONE."}},
			Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
				return map[string]any{"phone": a.Str("phone"), "hosts": p.ResolveAll(ctx, a.Str("phone"))}, nil
			},
		},
		{
			Name: "wsm_hosts_status", ReadOnly: true, Idempotent: true,
			Desc: "Check every configured host's WSM /api/v1/health and waxum Health in one call, with round-trip latency, so you can tell at a glance which host (if any) is reachable before routing other calls through target.",
			Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
				out := []map[string]any{}
				for _, h := range p.Hosts {
					entry := map[string]any{"host": h.Name}
					wsmStart := time.Now()
					wsmRes, err := p.CallWSM(ctx, "GET", "/api/v1/health", nil, nil, nil, false, h.Name)
					if err != nil {
						entry["wsm_error"] = err.Error()
					} else {
						entry["wsm_status"] = wsmRes.Status
						entry["wsm_body"] = wsmRes.Body
					}
					entry["wsm_latency_ms"] = time.Since(wsmStart).Milliseconds()

					waxumStart := time.Now()
					waxumErr := h.WaxumClient().Health(ctx)
					if waxumErr != nil {
						entry["waxum_error"] = waxumErr.Error()
						entry["waxum_reachable"] = false
					} else {
						entry["waxum_reachable"] = true
					}
					entry["waxum_latency_ms"] = time.Since(waxumStart).Milliseconds()
					out = append(out, entry)
				}
				return map[string]any{"hosts": out}, nil
			},
		},
	}
}
