package handler

import (
	"io/fs"
	"net/http"
)

// mountUI registers the operator's browser surface under d.BasePath.
//
// Every pattern below is built as bp+"/path". With BasePath="" that yields the
// original route table byte for byte, which is what keeps `make run-wsm` and the
// handler tests working unchanged; with "/wsm" the whole surface moves and the
// templates follow it through the `u` template function (internal/renderer).
//
// GET / IS THE ONE PATTERN THAT NEEDS CARE. Mounted, the front door is
// "GET /wsm/{$}" — and a browser that asks for "/wsm" without the trailing
// slash does NOT match it. Go's ServeMux redirects /wsm → /wsm/ automatically
// only for subtree patterns ending in "/", so the bare-prefix redirect is
// registered explicitly below. Without it, the sidebar link from the backoffice
// lands on a 404 while every path underneath works — the kind of gap that reads
// as "the whole thing is broken" from one link and "fine" from everywhere else.
func (h *handlers) mountUI(mux *http.ServeMux) {
	bp := h.BasePath
	page := h.Middleware.RequirePage

	// Shell.
	mux.HandleFunc("GET "+bp+"/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, bp+"/dashboard", http.StatusFound)
	})
	if bp != "" {
		// "/wsm" (no trailing slash) → "/wsm/". See the note above.
		mux.HandleFunc("GET "+bp, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, bp+"/", http.StatusMovedPermanently)
		})
	}
	// The door itself: both registered OUTSIDE page(...), like /theme/toggle.
	mux.HandleFunc("GET "+bp+"/login", h.GetLogin)   // local admin sign-in form
	mux.HandleFunc("POST "+bp+"/login", h.PostLogin) // verify + mint session
	mux.Handle("POST "+bp+"/logout", page(http.HandlerFunc(h.PostLogout)))
	mux.Handle("GET "+bp+"/dashboard", page(http.HandlerFunc(h.GetDashboard)))
	mux.Handle("GET "+bp+"/dashboard/widgets", page(http.HandlerFunc(h.GetWidgets)))

	// Outbound message log.
	mux.Handle("GET "+bp+"/logs", page(http.HandlerFunc(h.GetLogs)))
	mux.Handle("GET "+bp+"/logs/rows", page(http.HandlerFunc(h.GetLogRows)))

	// Outgoing Chat: per-session send-pacing visibility (real gap stats
	// from wa_messages.sent_at vs. the configured WSM_SEND_RATE/BURST).
	mux.Handle("GET "+bp+"/outgoing-chat", page(http.HandlerFunc(h.GetOutgoingChat)))
	mux.Handle("GET "+bp+"/outgoing-chat/rows", page(http.HandlerFunc(h.GetOutgoingChatRows)))

	// System log: in-memory diagnostic events (internal/wa's Manager.RecentEvents),
	// NOT the same thing as the outbound message log above — see system_log.go's
	// doc comment.
	mux.Handle("GET "+bp+"/system-log", page(http.HandlerFunc(h.GetSystemLog)))
	mux.Handle("GET "+bp+"/system-log/rows", page(http.HandlerFunc(h.GetSystemLogRows)))
	if h.DarkMode {
		mux.HandleFunc("POST "+bp+"/theme/toggle", h.PostThemeToggle)
	}

	// API clients.
	mux.Handle("GET "+bp+"/admin/api-clients", page(http.HandlerFunc(h.GetAPIClients)))
	mux.Handle("POST "+bp+"/admin/api-clients", page(http.HandlerFunc(h.PostAPIClientCreate)))
	mux.Handle("GET "+bp+"/admin/api-clients/{id}/edit", page(http.HandlerFunc(h.GetAPIClientEdit)))
	mux.Handle("POST "+bp+"/admin/api-clients/{id}/edit", page(http.HandlerFunc(h.PostAPIClientEdit)))
	mux.Handle("POST "+bp+"/admin/api-clients/{id}/activate", page(http.HandlerFunc(h.PostAPIClientActivate)))
	mux.Handle("POST "+bp+"/admin/api-clients/{id}/deactivate", page(http.HandlerFunc(h.PostAPIClientDeactivate)))
	mux.Handle("POST "+bp+"/admin/api-clients/{id}/rotate", page(http.HandlerFunc(h.PostAPIClientRotate)))
	mux.Handle("POST "+bp+"/admin/api-clients/{id}/delete", page(http.HandlerFunc(h.PostAPIClientDelete)))

	// Sessions + pairing pages and partials.
	mux.Handle("GET "+bp+"/sessions", page(http.HandlerFunc(h.GetSessions)))
	mux.Handle("GET "+bp+"/sessions/rows", page(http.HandlerFunc(h.GetSessionRows)))
	mux.Handle("GET "+bp+"/sessions/new", page(http.HandlerFunc(h.GetSessionNew)))
	mux.Handle("POST "+bp+"/sessions", page(http.HandlerFunc(h.PostSessionCreate)))
	mux.Handle("POST "+bp+"/sessions/{id}/activate", page(http.HandlerFunc(h.PostSessionActivate)))
	mux.Handle("POST "+bp+"/sessions/{id}/deactivate", page(http.HandlerFunc(h.PostSessionDeactivate)))
	mux.Handle("POST "+bp+"/sessions/{id}/master", page(http.HandlerFunc(h.PostSessionSetMaster)))
	mux.Handle("POST "+bp+"/sessions/{id}/group-code", page(http.HandlerFunc(h.PostSessionSetGroupCode)))
	mux.Handle("GET "+bp+"/sessions/{id}/group-code/edit", page(http.HandlerFunc(h.GetSessionGroupCodeEditor)))
	mux.Handle("POST "+bp+"/sessions/{id}/delete", page(http.HandlerFunc(h.PostSessionDelete)))
	mux.Handle("GET "+bp+"/sessions/{id}/pair", page(http.HandlerFunc(h.GetPair)))
	mux.Handle("POST "+bp+"/sessions/{id}/pair/start", page(http.HandlerFunc(h.PostPairStart)))
	mux.Handle("GET "+bp+"/sessions/{id}/pair/status", page(http.HandlerFunc(h.GetPairStatus)))
	mux.Handle("GET "+bp+"/sessions/{id}/qr.png", page(http.HandlerFunc(h.GetQRPNG)))

	// Send page.
	mux.Handle("GET "+bp+"/send", page(http.HandlerFunc(h.GetSend)))
	mux.Handle("POST "+bp+"/send", page(http.HandlerFunc(h.PostSend)))
	mux.Handle("POST "+bp+"/send/media", page(http.HandlerFunc(h.PostSendMedia)))

	// Verify Number page.
	mux.Handle("GET "+bp+"/verify", page(http.HandlerFunc(h.GetVerify)))
	mux.Handle("POST "+bp+"/verify", page(http.HandlerFunc(h.PostVerify)))

	// Self Convo — simulated two-party conversations between two of this
	// deployment's own paired sessions. GET /self-convo/templates/example
	// MUST be registered BEFORE GET /self-convo/templates/{id}: Go 1.22's
	// mux prefers the more specific literal over a wildcard automatically,
	// but the order still matters to a reader and is what
	// TestTheExampleRouteIsNotShadowedByTheTemplateIdRoute pins.
	mux.Handle("GET "+bp+"/self-convo", page(http.HandlerFunc(h.GetSelfConvo)))
	mux.Handle("GET "+bp+"/self-convo/runs/rows", page(http.HandlerFunc(h.GetSelfConvoRunRows)))
	mux.Handle("POST "+bp+"/self-convo/runs", page(http.HandlerFunc(h.PostSelfConvoTrigger)))
	mux.Handle("POST "+bp+"/self-convo/runs/{id}/cancel", page(http.HandlerFunc(h.PostSelfConvoCancel)))
	mux.Handle("GET "+bp+"/self-convo/templates", page(http.HandlerFunc(h.GetSelfConvoTemplates)))
	mux.Handle("GET "+bp+"/self-convo/templates/rows", page(http.HandlerFunc(h.GetSelfConvoTemplateRows)))
	mux.Handle("POST "+bp+"/self-convo/templates", page(http.HandlerFunc(h.PostSelfConvoTemplateUpload)))
	mux.Handle("GET "+bp+"/self-convo/templates/example", page(http.HandlerFunc(h.GetSelfConvoExample)))
	mux.Handle("POST "+bp+"/self-convo/templates/{id}/rename", page(http.HandlerFunc(h.PostSelfConvoTemplateRename)))
	mux.Handle("POST "+bp+"/self-convo/templates/{id}/delete", page(http.HandlerFunc(h.PostSelfConvoTemplateDelete)))
	mux.Handle("GET "+bp+"/self-convo/templates/{id}", page(http.HandlerFunc(h.GetSelfConvoTemplateView)))
	mux.Handle("GET "+bp+"/self-convo/templates/{id}/download", page(http.HandlerFunc(h.GetSelfConvoTemplateDownload)))

	// Convo scheduling — recurring, multi-receiver Self Convo runs
	// (internal/domain/convo/schedule_model.go, internal/selfconvo/
	// planner.go). Same static-before-wildcard registration-order
	// convention as the templates block above: "new"/"rows" are literal
	// paths that must read as distinct from "{id}" even though Go 1.22's
	// mux would already resolve them correctly either way.
	mux.Handle("GET "+bp+"/self-convo/schedules", page(http.HandlerFunc(h.GetSelfConvoSchedules)))
	mux.Handle("GET "+bp+"/self-convo/schedules/rows", page(http.HandlerFunc(h.GetSelfConvoScheduleRows)))
	mux.Handle("GET "+bp+"/self-convo/schedules/new", page(http.HandlerFunc(h.GetSelfConvoScheduleNew)))
	mux.Handle("POST "+bp+"/self-convo/schedules", page(http.HandlerFunc(h.PostSelfConvoScheduleCreate)))
	mux.Handle("GET "+bp+"/self-convo/schedules/{id}/edit", page(http.HandlerFunc(h.GetSelfConvoScheduleEdit)))
	mux.Handle("POST "+bp+"/self-convo/schedules/{id}", page(http.HandlerFunc(h.PostSelfConvoScheduleUpdate)))
	mux.Handle("POST "+bp+"/self-convo/schedules/{id}/pause", page(http.HandlerFunc(h.PostSelfConvoSchedulePause)))
	mux.Handle("POST "+bp+"/self-convo/schedules/{id}/resume", page(http.HandlerFunc(h.PostSelfConvoScheduleResume)))
	mux.Handle("POST "+bp+"/self-convo/schedules/{id}/stop", page(http.HandlerFunc(h.PostSelfConvoScheduleStop)))
	mux.Handle("POST "+bp+"/self-convo/schedules/{id}/delete", page(http.HandlerFunc(h.PostSelfConvoScheduleDelete)))
	mux.Handle("GET "+bp+"/self-convo/schedules/{id}/runs/rows", page(http.HandlerFunc(h.GetSelfConvoScheduleRunRows)))
	mux.Handle("GET "+bp+"/self-convo/schedules/{id}", page(http.HandlerFunc(h.GetSelfConvoScheduleView)))

	// Known groups — per-session friendly-name-to-JID CRUD backing Group
	// Broadcast Schedule's group picker (internal/domain/knowngroup).
	mux.Handle("GET "+bp+"/groups", page(http.HandlerFunc(h.GetKnownGroups)))
	mux.Handle("GET "+bp+"/groups/rows", page(http.HandlerFunc(h.GetKnownGroupRows)))
	mux.Handle("POST "+bp+"/groups", page(http.HandlerFunc(h.PostKnownGroupCreate)))
	mux.Handle("POST "+bp+"/groups/{id}/delete", page(http.HandlerFunc(h.PostKnownGroupDelete)))
	mux.Handle("GET "+bp+"/groups/picker", page(http.HandlerFunc(h.GetKnownGroupPicker)))

	// Group Broadcast Schedule — same static-before-wildcard registration
	// order convention as the self-convo schedules block above.
	mux.Handle("GET "+bp+"/group-broadcast/schedules", page(http.HandlerFunc(h.GetGroupBroadcastSchedules)))
	mux.Handle("GET "+bp+"/group-broadcast/schedules/rows", page(http.HandlerFunc(h.GetGroupBroadcastScheduleRows)))
	mux.Handle("GET "+bp+"/group-broadcast/schedules/new", page(http.HandlerFunc(h.GetGroupBroadcastScheduleNew)))
	mux.Handle("POST "+bp+"/group-broadcast/schedules", page(http.HandlerFunc(h.PostGroupBroadcastScheduleCreate)))
	mux.Handle("GET "+bp+"/group-broadcast/schedules/{id}/edit", page(http.HandlerFunc(h.GetGroupBroadcastScheduleEdit)))
	mux.Handle("POST "+bp+"/group-broadcast/schedules/{id}", page(http.HandlerFunc(h.PostGroupBroadcastScheduleUpdate)))
	mux.Handle("POST "+bp+"/group-broadcast/schedules/{id}/pause", page(http.HandlerFunc(h.PostGroupBroadcastSchedulePause)))
	mux.Handle("POST "+bp+"/group-broadcast/schedules/{id}/resume", page(http.HandlerFunc(h.PostGroupBroadcastScheduleResume)))
	mux.Handle("POST "+bp+"/group-broadcast/schedules/{id}/stop", page(http.HandlerFunc(h.PostGroupBroadcastScheduleStop)))
	mux.Handle("POST "+bp+"/group-broadcast/schedules/{id}/delete", page(http.HandlerFunc(h.PostGroupBroadcastScheduleDelete)))
	mux.Handle("GET "+bp+"/group-broadcast/schedules/{id}/occurrences/rows", page(http.HandlerFunc(h.GetGroupBroadcastScheduleOccurrenceRows)))
	mux.Handle("GET "+bp+"/group-broadcast/schedules/{id}", page(http.HandlerFunc(h.GetGroupBroadcastScheduleView)))

	// Calls page.
	mux.Handle("GET "+bp+"/calls", page(http.HandlerFunc(h.GetCalls)))
	mux.Handle("GET "+bp+"/calls/rows", page(http.HandlerFunc(h.GetCallRows)))
	mux.Handle("POST "+bp+"/calls/tts", page(http.HandlerFunc(h.PostCallsTTS)))
	mux.Handle("POST "+bp+"/calls/play", page(http.HandlerFunc(h.PostCallsPlay)))
	mux.Handle("POST "+bp+"/calls/{id}/terminate", page(http.HandlerFunc(h.PostCallsTerminate)))

	// Per-session operations page — reached from a button in
	// partials/session_rows.html, NOT the sidebar (nav.go's buildNav):
	// there is no single "operations" destination, only one per session.
	mux.Handle("GET "+bp+"/sessions/{id}/ops", page(http.HandlerFunc(h.GetSessionOps)))
	mux.Handle("GET "+bp+"/sessions/{id}/ops/panel", page(http.HandlerFunc(h.GetSessionOpsPanel)))
	mux.Handle("POST "+bp+"/sessions/{id}/ops/pause", page(http.HandlerFunc(h.PostSessionOpsPause)))
	mux.Handle("POST "+bp+"/sessions/{id}/ops/resume", page(http.HandlerFunc(h.PostSessionOpsResume)))
	mux.Handle("POST "+bp+"/sessions/{id}/ops/auto-reconnect/enable", page(http.HandlerFunc(h.PostSessionOpsAutoReconnectEnable)))
	mux.Handle("POST "+bp+"/sessions/{id}/ops/auto-reconnect/disable", page(http.HandlerFunc(h.PostSessionOpsAutoReconnectDisable)))
	mux.Handle("POST "+bp+"/sessions/{id}/ops/history-sync/enable", page(http.HandlerFunc(h.PostSessionOpsHistorySyncEnable)))
	mux.Handle("POST "+bp+"/sessions/{id}/ops/history-sync/disable", page(http.HandlerFunc(h.PostSessionOpsHistorySyncDisable)))
	mux.Handle("POST "+bp+"/sessions/{id}/ops/appstate-resync", page(http.HandlerFunc(h.PostSessionOpsAppStateResync)))

	// Per-session webhooks page — same "not the sidebar" rule as ops above.
	mux.Handle("GET "+bp+"/sessions/{id}/webhooks", page(http.HandlerFunc(h.GetSessionWebhooks)))
	mux.Handle("GET "+bp+"/sessions/{id}/webhooks/rows", page(http.HandlerFunc(h.GetSessionWebhookRows)))
	mux.Handle("POST "+bp+"/sessions/{id}/webhooks", page(http.HandlerFunc(h.PostSessionWebhookRegister)))
	mux.Handle("POST "+bp+"/sessions/{id}/webhooks/{webhookID}/delete", page(http.HandlerFunc(h.PostSessionWebhookDelete)))
	mux.Handle("POST "+bp+"/sessions/{id}/webhooks/{webhookID}/enable", page(http.HandlerFunc(h.PostSessionWebhookEnable)))
	mux.Handle("GET "+bp+"/sessions/{id}/webhooks/dlq/rows", page(http.HandlerFunc(h.GetSessionWebhookDLQRows)))
	mux.Handle("POST "+bp+"/sessions/{id}/webhooks/dlq/{entryID}/replay", page(http.HandlerFunc(h.PostSessionWebhookDLQReplay)))

	// Static assets (embedded). StripPrefix carries bp too — the files are
	// addressed inside the embedded FS, which knows nothing about the mount.
	staticFS, err := fs.Sub(h.AssetsFS, "web/static")
	if err != nil {
		panic(err) // embed layout is fixed at compile time
	}
	mux.Handle("GET "+bp+"/static/", http.StripPrefix(bp+"/static/", http.FileServerFS(staticFS)))

	// Swagger UI is part of the browser surface and moves with it. The spec it
	// fetches does NOT — see routes_api.go.
	mux.HandleFunc("GET "+bp+"/docs", h.GetDocs)
}
