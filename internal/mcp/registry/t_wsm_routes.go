package registry

import (
	"context"
	"fmt"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
)

// sessionParams/targetParam/bodyParam/queryParam are the Param building
// blocks every generic wsm_* tool below is assembled from — see
// routeTool's doc comment for how they combine.
var (
	sessionIDParam = Param{Name: "session_id", Type: "string", Desc: "WSM session id. Either this or phone is required for session-scoped routes; if both are given, session_id wins. Per-host: the SAME phone has a DIFFERENT session_id on each configured host (see wsm_resolve_session)."}
	phoneParam     = Param{Name: "phone", Type: "string", Desc: "Phone number (any of 62xxxx, 0xxxx, +62xxxx) used to resolve a session_id when none is given. Defaults to WSM_MCP_DEFAULT_PHONE when neither session_id nor phone is given."}
	targetParam    = Param{Name: "target", Type: "string", Desc: "Pin this call to one configured host by name (as listed in WSM_MCP_HOSTS), disabling failover for this call."}
	bodyParam      = Param{Name: "body", Type: "string", Desc: "JSON object request body, forwarded verbatim to WSM. See openapi.yaml for this route's exact schema."}
	queryParam     = Param{Name: "query", Type: "string", Desc: "JSON object of string query parameters, forwarded verbatim to WSM."}
)

// result wraps a WSM call's outcome in the shape every wsm_* tool returns:
// {"host": "...", "status": N, "body": <decoded JSON>}.
func wsmResultOut(r upstream.WSMResult) any {
	return map[string]any{"host": r.Host, "status": r.Status, "body": r.Body}
}

// bodyArg decodes the "body" argument (a JSON-object string) into a
// generic map, or nil when absent — Args carries JSON-decoded values, and
// a nested object argument arrives as map[string]any directly when the
// caller passes a real JSON object rather than a string, so both shapes
// are accepted.
func objArg(a Args, key string) (map[string]any, error) {
	switch v := a[key].(type) {
	case nil:
		return nil, nil
	case map[string]any:
		return v, nil
	case string:
		if v == "" {
			return nil, nil
		}
		return nil, fmt.Errorf("%s must be a JSON object, not a string — pass it as a nested object", key)
	default:
		return nil, fmt.Errorf("%s must be a JSON object", key)
	}
}

func queryArg(a Args, key string) (map[string]string, error) {
	obj, err := objArg(a, key)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		if s, ok := v.(string); ok {
			out[k] = s
		} else {
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out, nil
}

// routeSpec describes one generic WSM route tool declaratively; wsmRouteTools
// turns each into a Tool whose Run resolves session_id/phone when the route
// has an {id} path segment, substitutes any other named path params from
// args, and forwards "body"/"query" verbatim.
type routeSpec struct {
	name, desc    string
	method, route string
	pathParams    []string // extra path param names beyond "id", e.g. "webhookID", "jid"
	hasBody       bool
	hasQuery      bool
	danger        bool
	readOnly      bool
	idempotent    bool
}

func (s routeSpec) tool() Tool {
	params := []Param{}
	sessionScoped := false
	for _, p := range []string{"id"} {
		if containsPathVar(s.route, p) {
			sessionScoped = true
		}
	}
	if sessionScoped {
		params = append(params, sessionIDParam, phoneParam)
	}
	for _, pp := range s.pathParams {
		params = append(params, Param{Name: pp, Type: "string", Desc: "Path parameter: " + pp, Required: true})
	}
	params = append(params, targetParam)
	if s.hasBody {
		params = append(params, bodyParam)
	}
	if s.hasQuery {
		params = append(params, queryParam)
	}
	write := s.method != "GET" && s.method != "HEAD"
	return Tool{
		Name: s.name, Desc: s.desc, Route: s.method + " " + s.route, Params: params,
		Danger: s.danger, ReadOnly: s.readOnly, Idempotent: s.idempotent,
		Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
			pathParams := map[string]string{}
			target := a.Str("target")
			if sessionScoped {
				h, id, err := p.ResolveSession(ctx, a.Str("session_id"), a.Str("phone"), target)
				if err != nil {
					return nil, err
				}
				pathParams["id"] = id
				target = h.Name
			}
			for _, pp := range s.pathParams {
				v := a.Str(pp)
				if v == "" {
					return nil, fmt.Errorf("missing required argument %q", pp)
				}
				pathParams[pp] = v
			}
			var body any
			if s.hasBody {
				b, err := objArg(a, "body")
				if err != nil {
					return nil, err
				}
				body = b
			}
			var query map[string]string
			if s.hasQuery {
				q, err := queryArg(a, "query")
				if err != nil {
					return nil, err
				}
				query = q
			}
			res, err := p.CallWSM(ctx, s.method, s.route, pathParams, query, body, write, target)
			if err != nil {
				return nil, err
			}
			return wsmResultOut(res), nil
		},
	}
}

func containsPathVar(route, name string) bool {
	return containsSub(route, "{"+name+"}")
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// wsmRouteTools returns one Tool per /api/v1 route in routes_api.go,
// excluding POST /api/v1/auth/login and POST /api/v1/auth/logout (this MCP
// never needs a bearer token — it authenticates every call with HTTP Basic
// directly) and GET /openapi.yaml (describes the spec, isn't part of it).
// sync_test.go's TestEveryAPIRouteHasAnMCPTool enforces this list stays
// exhaustive; see its exemption comment for exactly those three.
func wsmRouteTools() []Tool {
	specs := []routeSpec{
		{name: "wsm_stats", desc: "Dashboard KPI snapshot (label -> formatted value) from WSM. Read-only, safe to call anytime.", method: "GET", route: "/api/v1/stats", readOnly: true, idempotent: true},
		{name: "wsm_health", desc: "WSM's own /api/v1/health: session counts, backend identity, reachability. No credential is required by WSM itself, but this tool still authenticates like every other. Use wsm_hosts_status to check both configured hosts at once.", method: "GET", route: "/api/v1/health", readOnly: true, idempotent: true},
		{name: "wsm_sessions_list", desc: "List every WSM session with its live state, phone, jid, master/group-code routing flags and queue length. The best first call to discover what session_id values exist on a host.", method: "GET", route: "/api/v1/sessions", readOnly: true, idempotent: true},
		{name: "wsm_sessions_create", desc: "Create a new WSM session (state=new; must be paired separately via wsm_sessions_pairing). body: {name, phone}. Not gated as Danger — creating a session does not touch a live device — but it is NOT a read either.", method: "POST", route: "/api/v1/sessions", hasBody: true},
		{name: "wsm_session_get", desc: "Fetch one session's live snapshot (state, phone, jid, connected, queue_len, master/group-code).", method: "GET", route: "/api/v1/sessions/{id}", readOnly: true, idempotent: true},
		{name: "wsm_session_delete", desc: "DANGER: delete a session. On the default waxum backend this does NOT unlink the paired device (waxum has no unlink API) — it disappears from the dashboard while staying in the handset's Linked Devices list, burning a linked-device slot. Gated by WSM_MCP_ALLOW_DANGEROUS.", method: "DELETE", route: "/api/v1/sessions/{id}", danger: true},
		{name: "wsm_session_activate", desc: "Activate (connect) a session.", method: "POST", route: "/api/v1/sessions/{id}/activate"},
		{name: "wsm_session_deactivate", desc: "Deactivate (disconnect) a session. Caution: a Deactivate immediately followed by Activate has historically wedged waxum sessions (see CLAUDE.md's Deactivate-then-Activate hazard) — this tool does not add any delay for you.", method: "POST", route: "/api/v1/sessions/{id}/deactivate"},
		{name: "wsm_session_set_master", desc: "Set/clear a session's is_master routing-exclusion flag. body: {is_master: bool}. A master session stays connected and directly sendable but is skipped by automatic routing unless allow_master is set on the send.", method: "PUT", route: "/api/v1/sessions/{id}/master", hasBody: true},
		{name: "wsm_session_set_group_code", desc: "Set/clear a session's group_code routing scope. body: {group_code: string}. Automatic routing only matches sessions carrying the SAME group_code; \"\" means ungrouped.", method: "PUT", route: "/api/v1/sessions/{id}/group-code", hasBody: true},
		{name: "wsm_session_pairing", desc: "Get the current pairing state for a session (QR/pair-code, mode, last error). Use when a session needs to be (re)paired.", method: "GET", route: "/api/v1/sessions/{id}/pairing", readOnly: true},

		{name: "wsm_send_message_auto", desc: "Send a text message using automatic session routing (no session_id needed; WSM picks a connected session, optionally scoped by group_code). body: {to, text, group_code?, allow_master?, client_ref?, tenant_ref?, category?}. Not Danger-gated: this is the normal send path and goes through WSM's own auth/routing/queue/log.", method: "POST", route: "/api/v1/messages", hasBody: true},
		{name: "wsm_send_group_message", desc: "Send a text message to a WhatsApp group using automatic routing. body: {to (group jid), text, ...}.", method: "POST", route: "/api/v1/messages/group", hasBody: true},
		{name: "wsm_session_send_group_message", desc: "Send a text message to a WhatsApp GROUP through one explicit session (resolve by session_id or phone) — it can never leave from another session, unlike wsm_send_group_message which picks one. The session's device must be a member of the group. body: {group_jid, text, ...}.", method: "POST", route: "/api/v1/sessions/{id}/messages/group", hasBody: true},
		{name: "wsm_session_send_message", desc: "Send a text message through one explicit session (resolve by session_id or phone). body: {to, text, ...}.", method: "POST", route: "/api/v1/sessions/{id}/messages", hasBody: true},
		{name: "wsm_session_messages", desc: "Read a session's sent-message log (paginate with query: {after, limit}).", method: "GET", route: "/api/v1/sessions/{id}/messages", readOnly: true, idempotent: true, hasQuery: true},
		{name: "wsm_session_groups", desc: "List the WhatsApp groups a session's paired device belongs to.", method: "GET", route: "/api/v1/sessions/{id}/groups", readOnly: true, idempotent: true},

		{name: "wsm_session_send_media_download", desc: "Download a previously received media attachment via this session. body: {media_id or descriptor fields — see openapi.yaml}.", method: "POST", route: "/api/v1/sessions/{id}/media/download", hasBody: true},

		{name: "wsm_session_calls_tts", desc: "DANGER: place a real outbound WhatsApp call and speak text-to-speech into it, through this session. 501 unless WSM_CALLS_ENABLED=true in the target deployment. body: {to, text, voice?}. Automated outbound calling risks a device ban (CLAUDE.md).", method: "POST", route: "/api/v1/sessions/{id}/calls/tts", hasBody: true, danger: true},
		{name: "wsm_session_calls_play", desc: "DANGER: place a real outbound WhatsApp call and play an audio URL into it. 501 unless WSM_CALLS_ENABLED=true. body: {to, audio_url}.", method: "POST", route: "/api/v1/sessions/{id}/calls/play", hasBody: true, danger: true},
		{name: "wsm_session_calls_terminate", desc: "DANGER: terminate an in-progress outbound call placed through this session. body: {peer?, call_id?, reason?}.", method: "POST", route: "/api/v1/sessions/{id}/calls/terminate", hasBody: true, danger: true},
		{name: "wsm_session_calls_list", desc: "List recent outbound call attempts for a session.", method: "GET", route: "/api/v1/sessions/{id}/calls", readOnly: true, idempotent: true},

		{name: "wsm_session_operations", desc: "Report which optional session operations (pause/resume, history-sync toggle, appstate resync, trust tokens, ...) the active backend supports. 501-shaped fields mean 'not supported', not 'failed'.", method: "GET", route: "/api/v1/sessions/{id}/operations", readOnly: true, idempotent: true},
		{name: "wsm_session_pause", desc: "Pause a session's send queue without disconnecting it.", method: "POST", route: "/api/v1/sessions/{id}/pause"},
		{name: "wsm_session_resume", desc: "Resume a paused session's send queue.", method: "POST", route: "/api/v1/sessions/{id}/resume"},
		{name: "wsm_session_auto_reconnect_get", desc: "Read whether automatic reconnect is enabled for a session.", method: "GET", route: "/api/v1/sessions/{id}/auto-reconnect", readOnly: true, idempotent: true},
		{name: "wsm_session_auto_reconnect_set", desc: "Enable/disable automatic reconnect for a session. body: {enabled: bool}.", method: "PUT", route: "/api/v1/sessions/{id}/auto-reconnect", hasBody: true},
		{name: "wsm_session_history_sync_get", desc: "Read whether chat-history sync is being skipped for a session.", method: "GET", route: "/api/v1/sessions/{id}/history-sync", readOnly: true, idempotent: true},
		{name: "wsm_session_history_sync_set", desc: "Set whether chat-history sync should be skipped for a session. body: {skip: bool}.", method: "PUT", route: "/api/v1/sessions/{id}/history-sync", hasBody: true},
		{name: "wsm_session_appstate_resync", desc: "Force an app-state resync (contacts, chat metadata) for a session. body: {collections?: [string], mode?: string}.", method: "POST", route: "/api/v1/sessions/{id}/appstate/resync", hasBody: true},
		{name: "wsm_session_spam_report", desc: "DANGER: report a contact/message as spam through this session to WhatsApp. This is a real, irreversible report against the live device identity. body: see openapi.yaml.", method: "POST", route: "/api/v1/sessions/{id}/spam-report", hasBody: true, danger: true},
		{name: "wsm_session_trust_tokens_issue", desc: "Issue trust-establishment tokens for one or more JIDs on a session. body: {jids: [string]}.", method: "POST", route: "/api/v1/sessions/{id}/trust-tokens", hasBody: true},
		{name: "wsm_session_trust_tokens_list", desc: "List trust-token JIDs known to a session.", method: "GET", route: "/api/v1/sessions/{id}/trust-tokens", readOnly: true, idempotent: true},
		{name: "wsm_session_trust_token_get", desc: "Get trust-token detail for one JID on a session.", method: "GET", route: "/api/v1/sessions/{id}/trust-tokens/{jid}", pathParams: []string{"jid"}, readOnly: true, idempotent: true},
		{name: "wsm_session_trust_tokens_prune_expired", desc: "Delete expired trust tokens for a session.", method: "DELETE", route: "/api/v1/sessions/{id}/trust-tokens/expired"},

		{name: "wsm_session_webhooks_list", desc: "List webhooks registered on a session.", method: "GET", route: "/api/v1/sessions/{id}/webhooks", readOnly: true, idempotent: true},
		{name: "wsm_session_webhooks_register", desc: "DANGER: register a new webhook on a session. body: {url, secret?, events: [string]}. The target URL must not resolve to loopback/RFC1918/ULA/CGNAT/link-local if this is forwarded to waxum (SSRF guard). A webhook pointed at a private address will be refused by the backend, not by this tool.", method: "POST", route: "/api/v1/sessions/{id}/webhooks", hasBody: true, danger: true},
		{name: "wsm_session_webhook_delete", desc: "DANGER: delete a registered webhook.", method: "DELETE", route: "/api/v1/sessions/{id}/webhooks/{webhookID}", pathParams: []string{"webhookID"}, danger: true},
		{name: "wsm_session_webhook_enable", desc: "Re-enable a previously disabled webhook.", method: "POST", route: "/api/v1/sessions/{id}/webhooks/{webhookID}/enable", pathParams: []string{"webhookID"}},
		{name: "wsm_session_webhooks_dlq", desc: "List a session's webhook dead-letter queue (deliveries that exhausted retries).", method: "GET", route: "/api/v1/sessions/{id}/webhooks/dlq", readOnly: true, idempotent: true},
		{name: "wsm_session_webhook_dlq_replay", desc: "Replay one dead-lettered webhook delivery.", method: "POST", route: "/api/v1/sessions/{id}/webhooks/dlq/{entryID}/replay", pathParams: []string{"entryID"}},
		{name: "wsm_webhooks_reenable_all", desc: "Re-enable every disabled webhook across every session on this host.", method: "POST", route: "/api/v1/webhooks/reenable-all"},

		{name: "wsm_gateway_health", desc: "Authenticated gateway health (unlike wsm_health/GET /api/v1/health, which needs no credential).", method: "GET", route: "/api/v1/gateway/health", readOnly: true, idempotent: true},
		{name: "wsm_inbound", desc: "Read inbound customer replies (wa_inbound_messages) for a polling consumer. query: {since?, limit?}. Use this to check 'did anyone reply since T'.", method: "GET", route: "/api/v1/inbound", readOnly: true, hasQuery: true},

		{name: "wsm_contacts_check_auto", desc: "Check whether one or more phone numbers are registered on WhatsApp, using automatic session routing. body: {phones: [string], group_code?, allow_master?}. 501 on a backend with no ContactDirectory support (e.g. whatsmeow).", method: "POST", route: "/api/v1/contacts/check", hasBody: true},
		{name: "wsm_session_contacts_check", desc: "Check whether one or more phone numbers are registered on WhatsApp, through one explicit session. body: {phones: [string]}.", method: "POST", route: "/api/v1/sessions/{id}/contacts/check", hasBody: true},

		// Contacts (internal/domain/contact): the owner-managed, GLOBAL
		// (not per-session) WhatsApp contact book. The {contactID} path
		// segment is deliberately NOT named "{id}" in routes_api.go, so
		// routeSpec.tool()'s containsPathVar(s.route, "id") check (above)
		// does NOT fire for these three — a contact id is not a session
		// id, and resolving it via session_id/phone would be wrong. See
		// also waxum_contacts (t_waxum_methods.go) for the RANKED read
		// that combines this list with waxum's own synced contacts.
		{name: "wsm_contacts_list", desc: "List the owner-managed contact book, or (with query: {q}) rank-search it by name/alias via internal/contactmatch — the same ranking waxum_contacts applies on top of waxum's own synced contacts. GLOBAL, not session-scoped.", method: "GET", route: "/api/v1/contacts", hasQuery: true, readOnly: true, idempotent: true},
		{name: "wsm_contacts_create", desc: "Save a new contact (body: {name, phone, aliases?: [string]}) and immediately verify it on WhatsApp through an auto-routed connected session. The create always succeeds even if that verify step fails; a failure is reported in the response's verify_error and leaves wa_status \"unknown\", never \"not_registered\".", method: "POST", route: "/api/v1/contacts", hasBody: true},
		{name: "wsm_contacts_delete", desc: "DANGER: delete a saved contact from WSM's own book. Does NOT touch any WhatsApp device or linked-device list — this is WSM-local bookkeeping only.", method: "DELETE", route: "/api/v1/contacts/{contactID}", pathParams: []string{"contactID"}, danger: true},
		{name: "wsm_contacts_verify", desc: "Re-verify a saved contact's phone on WhatsApp through an auto-routed connected session. A routing/backend failure leaves wa_status \"unknown\" (reported in verify_error), never fabricated as \"not_registered\".", method: "POST", route: "/api/v1/contacts/{contactID}/verify", pathParams: []string{"contactID"}},
	}
	out := make([]Tool, 0, len(specs)+2)
	for _, s := range specs {
		out = append(out, s.tool())
	}
	out = append(out, mediaSendTools()...)
	return out
}
