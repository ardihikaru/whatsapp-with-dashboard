package handler

import "net/http"

// mountAPI registers the JSON API at the ROOT — deliberately NOT under
// BasePath — plus /openapi.yaml, which the Swagger UI fetches.
//
// ── WHY THIS DOES NOT MOVE WITH THE UI ──────────────────────────────────────
//
// Four callers dial these paths, and none of them goes through the ingress:
//
//	wa-alerter                     WSM_URL + /api/v1/messages
//	netdata's wa-notify.sh         WSM_URL + /api/v1/messages
//	backoffice (notification send) WSM_URL + /api/v1/messages
//	the container healthcheck      /api/v1/health
//
// All four resolve WSM by container DNS on the private network. Prefixing
// /api/v1 would break every one of them to gain nothing, because none of them
// can reach a public path anyway.
//
// It is also what keeps the mount collision-free by construction. Traefik
// routes only PathPrefix(`/wsm`) to this service, so /api/v1 here can never be
// confused with core-api's /api on the same origin — they are different
// services behind different routers, and this one has no public route at all.
//
// ⚠ REGISTERING A ROUTE HERE DOES NOT PUBLISH IT. Nothing in this file is
// reachable from the internet by default. The production edge
// (docs/deploy-nginx-wa.conf.example, live as /etc/nginx/conf.d/wa.conf on the
// public box) answers 404 for every /api/v1 path except an explicit allowlist,
// and a route added below stays private until that file is edited and nginx
// reloaded. Published as of 2026-09-10:
//
//	POST /api/v1/auth/login, POST /api/v1/auth/logout
//	POST /api/v1/messages, /messages/group, /messages/media
//	GET  /api/v1/inbound
//	POST /api/v1/contacts/check
//	     /api/v1/sessions/{id}/messages, /messages/media, /contacts/check
//	GET  /api/v1/sessions, /api/v1/sessions/{id}   read-only session views.
//	GET  /api/v1/health                            no credential at all — see below.
//
// nginx matches a location by PATH, not method, so a published PATH is not the
// same thing as a published METHOD. Two routes below share a path with one of
// the reads just published, and stay unreachable at the edge only because their
// location carries its own $request_method gate, not because the path is
// absent:
//
//	POST /sessions           path published (GET/HEAD only, the list above);
//	                          this method 404s at the edge. Creating a session
//	                          is gateway administration, not use of it.
//	DELETE /sessions/{id}    path published (GET/HEAD only, the detail view
//	                          above); this method 404s at the edge. It cannot
//	                          unlink the device on the waxum backend and burns
//	                          a linked-device slot per call (CLAUDE.md).
//
// Everything else is deliberately unpublished, and one is worth naming because
// the reason is not obvious from the handler:
//
//	/sessions/{id}/calls/*    places real outbound calls; WSM_CALLS_ENABLED is
//	                          true in production and a ban lands on a device
//	                          identity with no second copy anywhere.
//
// GET /api/v1/health is the ONLY route below with no RequireAPI wrapper — it
// answers unauthenticated and reports session counts, backend identity and
// reachability. It is published anyway (deliberate choice, for uptime probing
// from outside the WireGuard tunnel), gated to GET/HEAD, and rate-limited by
// its own tighter zone precisely because there is no credential to protect it
// with — see docs/deploy-nginx-wa.conf.example's wa_health zone.
//
// Before Agustus 2026 a bare Host(`wsm.$DOMAIN`) router exposed the whole
// service, JSON API included, to the internet — arguably a bug rather than a
// feature. That is what the allowlist replaces. Widening it is a security
// decision, not a routing tweak: say why here, add the location there, and
// update nginx_edge_test.go, which asserts the published set exactly.
//
// /openapi.yaml stays at the root with the API it describes. The Swagger UI at
// BasePath+"/docs" is a browser page and moves with the UI; the spec is a
// machine-readable artefact of the API and stays with it.
func (h *handlers) mountAPI(mux *http.ServeMux) {
	api := h.Middleware.RequireAPI

	mux.HandleFunc("GET /openapi.yaml", h.GetSpec)

	mux.HandleFunc("POST /api/v1/auth/login", h.PostAPILogin)
	mux.Handle("POST /api/v1/auth/logout", api(http.HandlerFunc(h.PostAPILogout)))
	mux.Handle("GET /api/v1/stats", api(http.HandlerFunc(h.GetStats)))
	mux.HandleFunc("GET /api/v1/health", h.GetHealth)
	mux.Handle("GET /api/v1/sessions", api(http.HandlerFunc(h.GetAPISessions)))
	mux.Handle("POST /api/v1/sessions", api(http.HandlerFunc(h.PostAPISessions)))
	mux.Handle("GET /api/v1/sessions/{id}", api(http.HandlerFunc(h.GetAPISession)))
	mux.Handle("DELETE /api/v1/sessions/{id}", api(http.HandlerFunc(h.DeleteAPISession)))
	mux.Handle("POST /api/v1/sessions/{id}/activate", api(http.HandlerFunc(h.PostAPISessionActivate)))
	mux.Handle("POST /api/v1/sessions/{id}/deactivate", api(http.HandlerFunc(h.PostAPISessionDeactivate)))
	// WSM-native routing state, not a backend capability — works on both
	// backends, unlike the Phase 6b §1 block below which all 501 on
	// whatsmeow. See PutAPISessionMaster's doc comment.
	mux.Handle("PUT /api/v1/sessions/{id}/master", api(http.HandlerFunc(h.PutAPISessionMaster)))
	// Same reasoning as /master immediately above: group_code is WSM-native
	// routing state, not a backend capability. See PutAPISessionGroupCode's
	// doc comment.
	mux.Handle("PUT /api/v1/sessions/{id}/group-code", api(http.HandlerFunc(h.PutAPISessionGroupCode)))
	mux.Handle("GET /api/v1/sessions/{id}/pairing", api(http.HandlerFunc(h.GetAPISessionPairing)))
	mux.Handle("POST /api/v1/messages", api(http.HandlerFunc(h.PostAPISendMessageAuto)))
	mux.Handle("POST /api/v1/messages/group", api(http.HandlerFunc(h.PostAPISendGroupMessage)))
	mux.Handle("POST /api/v1/sessions/{id}/messages", api(http.HandlerFunc(h.PostAPISendMessage)))
	mux.Handle("GET /api/v1/sessions/{id}/messages", api(http.HandlerFunc(h.GetAPISessionMessages)))
	mux.Handle("GET /api/v1/sessions/{id}/groups", api(http.HandlerFunc(h.GetAPISessionGroups)))

	// Phase 6a: media send/download.
	mux.Handle("POST /api/v1/sessions/{id}/messages/media", api(http.HandlerFunc(h.PostAPISessionSendMedia)))
	mux.Handle("POST /api/v1/messages/media", api(http.HandlerFunc(h.PostAPISendMediaAuto)))
	mux.Handle("POST /api/v1/sessions/{id}/media/download", api(http.HandlerFunc(h.PostAPISessionMediaDownload)))

	// Phase 6a: calls — 501 on every one of these until WSM_CALLS_ENABLED=true
	// (see api_calls.go's callsDisabledMessage).
	mux.Handle("POST /api/v1/sessions/{id}/calls/tts", api(http.HandlerFunc(h.PostAPICallTTS)))
	mux.Handle("POST /api/v1/sessions/{id}/calls/play", api(http.HandlerFunc(h.PostAPICallPlay)))
	mux.Handle("POST /api/v1/sessions/{id}/calls/terminate", api(http.HandlerFunc(h.PostAPICallTerminate)))
	mux.Handle("GET /api/v1/sessions/{id}/calls", api(http.HandlerFunc(h.GetAPISessionCalls)))

	// Phase 6b §1: session operations (capabilities.go's SessionOps) — 501
	// on every one of these on the whatsmeow backend. Route names are
	// WSM-native, not waxum's own — see api_ops.go's header comment.
	mux.Handle("GET /api/v1/sessions/{id}/operations", api(http.HandlerFunc(h.GetAPISessionOperations)))
	mux.Handle("POST /api/v1/sessions/{id}/pause", api(http.HandlerFunc(h.PostAPISessionPause)))
	mux.Handle("POST /api/v1/sessions/{id}/resume", api(http.HandlerFunc(h.PostAPISessionResume)))
	mux.Handle("GET /api/v1/sessions/{id}/auto-reconnect", api(http.HandlerFunc(h.GetAPISessionAutoReconnect)))
	mux.Handle("PUT /api/v1/sessions/{id}/auto-reconnect", api(http.HandlerFunc(h.PutAPISessionAutoReconnect)))
	mux.Handle("GET /api/v1/sessions/{id}/history-sync", api(http.HandlerFunc(h.GetAPISessionHistorySync)))
	mux.Handle("PUT /api/v1/sessions/{id}/history-sync", api(http.HandlerFunc(h.PutAPISessionHistorySync)))
	mux.Handle("POST /api/v1/sessions/{id}/appstate/resync", api(http.HandlerFunc(h.PostAPISessionAppStateResync)))
	mux.Handle("POST /api/v1/sessions/{id}/spam-report", api(http.HandlerFunc(h.PostAPISessionSpamReport)))
	mux.Handle("POST /api/v1/sessions/{id}/trust-tokens", api(http.HandlerFunc(h.PostAPISessionTrustTokens)))
	mux.Handle("GET /api/v1/sessions/{id}/trust-tokens", api(http.HandlerFunc(h.GetAPISessionTrustTokens)))
	mux.Handle("GET /api/v1/sessions/{id}/trust-tokens/{jid}", api(http.HandlerFunc(h.GetAPISessionTrustToken)))
	mux.Handle("DELETE /api/v1/sessions/{id}/trust-tokens/expired", api(http.HandlerFunc(h.DeleteAPISessionTrustTokensExpired)))

	// Phase 6b §2: webhook administration (capabilities.go's WebhookAdmin).
	mux.Handle("GET /api/v1/sessions/{id}/webhooks", api(http.HandlerFunc(h.GetAPISessionWebhooks)))
	mux.Handle("POST /api/v1/sessions/{id}/webhooks", api(http.HandlerFunc(h.PostAPISessionWebhooks)))
	mux.Handle("DELETE /api/v1/sessions/{id}/webhooks/{webhookID}", api(http.HandlerFunc(h.DeleteAPISessionWebhook)))
	mux.Handle("POST /api/v1/sessions/{id}/webhooks/{webhookID}/enable", api(http.HandlerFunc(h.PostAPISessionWebhookEnable)))
	mux.Handle("GET /api/v1/sessions/{id}/webhooks/dlq", api(http.HandlerFunc(h.GetAPISessionWebhookDLQ)))
	mux.Handle("POST /api/v1/sessions/{id}/webhooks/dlq/{entryID}/replay", api(http.HandlerFunc(h.PostAPISessionWebhookDLQReplay)))
	mux.Handle("POST /api/v1/webhooks/reenable-all", api(http.HandlerFunc(h.PostAPIWebhooksReenableAll)))

	// Phase 6b §3: gateway health — authenticated, unlike /api/v1/health.
	mux.Handle("GET /api/v1/gateway/health", api(http.HandlerFunc(h.GetAPIGatewayHealth)))

	// Inbound: a fleet-level read of customer replies (wa_inbound_messages)
	// for a polling consumer — see docs/brief-inbound-api.md and
	// api_inbound.go's doc comment. 503 when Deps.Inbound is unset.
	mux.Handle("GET /api/v1/inbound", api(http.HandlerFunc(h.GetAPIInbound)))

	// Number verification (wa.ContactDirectory) — 501 on a whatsmeow
	// backend, same as every other optional-capability route above. Auto
	// vs explicit-session mirrors /api/v1/messages'/
	// /api/v1/sessions/{id}/messages' own split.
	mux.Handle("POST /api/v1/contacts/check", api(http.HandlerFunc(h.PostAPIContactsCheckAuto)))
	mux.Handle("POST /api/v1/sessions/{id}/contacts/check", api(http.HandlerFunc(h.PostAPIContactsCheck)))
}
