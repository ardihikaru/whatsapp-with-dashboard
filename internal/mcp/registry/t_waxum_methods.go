package registry

import (
	"context"
	"fmt"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// waxumResultOut wraps a waxum call's outcome the same way wsmResultOut
// does for WSM: {"host": "...", "data": <method's return value>}.
func waxumResultOut(r upstream.WaxumResult) any {
	return map[string]any{"host": r.Host, "data": r.Data}
}

// resolveWaxum resolves session_id/phone/target the same way routeSpec
// does for WSM tools — the WSM session id IS the waxum session id
// (internal/wa/backend_waxum.go passes s.ID to waxum.CreateSession), so
// one resolver serves both tool families.
func resolveWaxum(ctx context.Context, p *upstream.Pool, a Args) (host string, id string, err error) {
	h, sid, err := p.ResolveSession(ctx, a.Str("session_id"), a.Str("phone"), a.Str("target"))
	if err != nil {
		return "", "", err
	}
	return h.Name, sid, nil
}

// waxumSpec declares one waxum_* tool backed by one *waxum.Client method.
// build turns decoded Args into the method's own argument list (excluding
// ctx and id, which resolveWaxum supplies); sessionScoped is false only for
// Health and ReenableAllWebhooks, which take no session id at all.
type waxumSpec struct {
	name, desc    string
	method        string
	extraParams   []Param
	sessionScoped bool
	danger        bool
	readOnly      bool
	idempotent    bool
	build         func(a Args) ([]any, error) // args AFTER id, in declaration order
}

func (s waxumSpec) tool() Tool {
	params := []Param{}
	if s.sessionScoped {
		params = append(params, sessionIDParam, phoneParam)
	}
	params = append(params, s.extraParams...)
	params = append(params, targetParam)
	write := s.danger || !s.readOnly
	return Tool{
		Name: s.name, Desc: s.desc, WaxumMethod: s.method, Params: params,
		Danger: s.danger, ReadOnly: s.readOnly, Idempotent: s.idempotent,
		Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
			extra, err := s.build(a)
			if err != nil {
				return nil, err
			}
			var host, id string
			if s.sessionScoped {
				host, id, err = resolveWaxum(ctx, p, a)
				if err != nil {
					return nil, err
				}
			} else {
				host = a.Str("target")
			}
			args := extra
			if s.sessionScoped {
				args = append([]any{id}, extra...)
			}
			res, err := p.CallWaxum(ctx, s.method, write, host, args...)
			if err != nil {
				return nil, err
			}
			return waxumResultOut(res), nil
		},
	}
}

func noArgs(Args) ([]any, error) { return nil, nil }

// waxumMethodTools returns one Tool per exported internal/waxum.Client
// method except TailEvents (wrapped separately as waxum_events_tail, since
// its signature is a streaming callback, not a value-returning call) and
// the two unexported helpers do/send (not reachable from outside the
// package at all, so there is nothing to wrap). sync_test.go's
// TestEveryWaxumClientMethodHasAnMCPTool enforces this list stays
// exhaustive; see its exemption comment for exactly those three.
func waxumMethodTools() []Tool {
	specs := []waxumSpec{
		{name: "waxum_check_contacts", method: "CheckContacts", desc: "Ask waxum directly (bypassing WSM's routing) whether phone numbers are registered on WhatsApp, through one session.", sessionScoped: true, readOnly: true,
			extraParams: []Param{{Name: "phones", Type: "array", Desc: "Phone numbers, bare digits.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.StrSlice("phones")}, nil }},
		{name: "waxum_create_session", method: "CreateSession", desc: "DANGER: create a session directly in waxum (bypassing WSM's own session bookkeeping). Normally use wsm_sessions_create instead; this exists for direct recovery/diagnosis. body-equivalent: id, name, reuse.", danger: true,
			extraParams: []Param{sessionIDParam, {Name: "name", Type: "string", Desc: "Session display name.", Required: true}, {Name: "reuse", Type: "boolean", Desc: "Reuse an existing store on disk for this id if one exists."}},
			build:       func(a Args) ([]any, error) { return nil, nil }, // id handled specially below
		},
		{name: "waxum_connect", method: "Connect", desc: "Ask waxum to (re)connect a session's websocket. Caution: see CLAUDE.md's Deactivate-then-Activate hazard before calling this right after waxum_disconnect.", sessionScoped: true,
			build: noArgs},
		{name: "waxum_disconnect", method: "Disconnect", desc: "DANGER: ask waxum to disconnect a session's websocket. Caution: see CLAUDE.md's Deactivate-then-Activate race before immediately reconnecting.", sessionScoped: true, danger: true,
			build: noArgs},
		{name: "waxum_delete_session", method: "DeleteSession", desc: "DANGER: delete a session in waxum directly. Does NOT unlink the paired device — waxum has no unlink API — so the device stays in the handset's Linked Devices list forever, burning a slot (CLAUDE.md).", sessionScoped: true, danger: true,
			build: noArgs},
		{name: "waxum_status", method: "Status", desc: "Raw waxum session status: state, is_logged_in, socket_alive, reachability, pairing attempt detail. This is the oracle for whether a session is really connected — see CLAUDE.md's 'a waxum session can wedge in connecting forever' hazard.", sessionScoped: true, readOnly: true, idempotent: true,
			build: noArgs},
		{name: "waxum_qr", method: "QR", desc: "Get the current pairing QR payload strings for a session (raw '2@...' strings, not PNGs — render them yourself).", sessionScoped: true, readOnly: true,
			build: noArgs},
		{name: "waxum_pair_code", method: "PairCode", desc: "Request a numeric pairing code for a session, to be typed into WhatsApp's 'Link with phone number' flow instead of scanning a QR. pair_phone is the phone being paired, which is normally the same number used to resolve session_id/phone above.", sessionScoped: true,
			extraParams: []Param{{Name: "pair_phone", Type: "string", Desc: "Phone number to pair (defaults to the resolved phone).", Required: false}, {Name: "notify", Type: "boolean", Desc: "Ask WhatsApp to push the code to the phone as a notification."}},
			build: func(a Args) ([]any, error) {
				pp := a.Str("pair_phone")
				if pp == "" {
					pp = a.Str("phone")
				}
				return []any{pp, a.Bool("notify")}, nil
			}},
		{name: "waxum_send_text", method: "SendText", desc: "DANGER (raw waxum path): send a text message directly through waxum, bypassing WSM's routing, queue and message log entirely. Prefer wsm_send_message_auto / wsm_session_send_message for a normal send.", sessionScoped: true, danger: true,
			extraParams: []Param{{Name: "to", Type: "string", Desc: "Recipient.", Required: true}, {Name: "text", Type: "string", Desc: "Message text.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.Str("to"), a.Str("text")}, nil }},
		{name: "waxum_send_media", method: "SendMedia", desc: "DANGER (raw waxum path): send a media message by URL directly through waxum. waxum's SSRF guard refuses a URL resolving to a private address. Prefer wsm_send_media_auto / wsm_session_send_media for a normal send.", sessionScoped: true, danger: true,
			extraParams: []Param{{Name: "kind", Type: "string", Desc: "Media kind.", Required: true, Enum: []string{"image", "video", "audio", "document"}}, {Name: "to", Type: "string", Desc: "Recipient.", Required: true}, {Name: "url", Type: "string", Desc: "Publicly reachable media URL.", Required: true}, {Name: "caption", Type: "string", Desc: "Optional caption."}},
			build: func(a Args) ([]any, error) {
				return []any{waxum.MediaKind(a.Str("kind")), a.Str("to"), a.Str("url"), a.Str("caption")}, nil
			}},
		{name: "waxum_groups", method: "Groups", desc: "List the WhatsApp groups a session's paired device belongs to, read directly from waxum.", sessionScoped: true, readOnly: true, idempotent: true,
			build: noArgs},
		{name: "waxum_health", method: "Health", desc: "waxum's own liveness check (no session scope). Use wsm_hosts_status to check both WSM and waxum on every configured host at once.", readOnly: true, idempotent: true,
			build: noArgs},
		{name: "waxum_pause", method: "Pause", desc: "Pause a session's send queue directly in waxum.", sessionScoped: true,
			build: noArgs},
		{name: "waxum_resume", method: "Resume", desc: "Resume a paused session's send queue directly in waxum.", sessionScoped: true,
			build: noArgs},
		{name: "waxum_auto_reconnect_get", method: "AutoReconnect", desc: "Read whether waxum's own auto-reconnect is enabled for a session.", sessionScoped: true, readOnly: true, idempotent: true,
			build: noArgs},
		{name: "waxum_auto_reconnect_set", method: "SetAutoReconnect", desc: "Enable/disable waxum's own auto-reconnect for a session.", sessionScoped: true,
			extraParams: []Param{{Name: "enabled", Type: "boolean", Desc: "Enable automatic reconnect.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.Bool("enabled")}, nil }},
		{name: "waxum_history_sync_get", method: "HistorySync", desc: "Read whether waxum is skipping chat-history sync for a session.", sessionScoped: true, readOnly: true, idempotent: true,
			build: noArgs},
		{name: "waxum_history_sync_set", method: "SetHistorySync", desc: "Set whether waxum should skip chat-history sync for a session.", sessionScoped: true,
			extraParams: []Param{{Name: "skip", Type: "boolean", Desc: "Skip history sync.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.Bool("skip")}, nil }},
		{name: "waxum_appstate_resync", method: "AppStateResync", desc: "Force waxum to resync app-state collections (contacts, chat metadata) for a session.", sessionScoped: true,
			extraParams: []Param{{Name: "collections", Type: "array", Desc: "Collection names to resync; empty means waxum's default set."}, {Name: "mode", Type: "string", Desc: "Resync mode."}},
			build:       func(a Args) ([]any, error) { return []any{a.StrSlice("collections"), a.Str("mode")}, nil }},
		{name: "waxum_report_spam", method: "ReportSpam", desc: "DANGER: report a message/contact as spam to WhatsApp directly through waxum. Real and irreversible against the live device identity.", sessionScoped: true, danger: true,
			extraParams: []Param{
				{Name: "message_id", Type: "string", Desc: "Reported message id."}, {Name: "message_timestamp", Type: "integer", Desc: "Reported message unix timestamp."},
				{Name: "from_jid", Type: "string", Desc: "Sender JID."}, {Name: "participant_jid", Type: "string", Desc: "Group participant JID, if applicable."},
				{Name: "group_jid", Type: "string", Desc: "Group JID, if applicable."}, {Name: "group_subject", Type: "string", Desc: "Group subject, if applicable."},
			},
			build: func(a Args) ([]any, error) {
				return []any{waxum.SpamReport{
					MessageID: a.Str("message_id"), MessageTimestamp: int64(a.Int("message_timestamp", 0)),
					FromJID: a.Str("from_jid"), ParticipantJID: a.Str("participant_jid"),
					GroupJID: a.Str("group_jid"), GroupSubject: a.Str("group_subject"),
				}}, nil
			}},
		{name: "waxum_tc_token_issue", method: "TCTokenIssue", desc: "Issue trust-establishment tokens for one or more JIDs, directly through waxum.", sessionScoped: true,
			extraParams: []Param{{Name: "jids", Type: "array", Desc: "JIDs to issue tokens for.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.StrSlice("jids")}, nil }},
		{name: "waxum_tc_token_get", method: "TCTokenGet", desc: "Get trust-token detail for one JID, directly through waxum.", sessionScoped: true, readOnly: true, idempotent: true,
			extraParams: []Param{{Name: "jid", Type: "string", Desc: "JID.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.Str("jid")}, nil }},
		{name: "waxum_tc_token_list", method: "TCTokenList", desc: "List trust-token JIDs known to a session, directly through waxum.", sessionScoped: true, readOnly: true, idempotent: true,
			build: noArgs},
		{name: "waxum_tc_token_prune", method: "TCTokenPrune", desc: "Delete expired trust tokens for a session, directly through waxum.", sessionScoped: true,
			build: noArgs},
		{name: "waxum_call_tts", method: "CallTTS", desc: "DANGER: place a real outbound WhatsApp call and speak text-to-speech into it, directly through waxum. Automated outbound calling risks a device ban (CLAUDE.md).", sessionScoped: true, danger: true,
			extraParams: []Param{{Name: "to", Type: "string", Desc: "Recipient.", Required: true}, {Name: "text", Type: "string", Desc: "Text to speak.", Required: true}, {Name: "voice", Type: "string", Desc: "TTS voice name; empty uses waxum's default."}},
			build:       func(a Args) ([]any, error) { return []any{a.Str("to"), a.Str("text"), a.Str("voice")}, nil }},
		{name: "waxum_call_play", method: "CallPlay", desc: "DANGER: place a real outbound WhatsApp call and play an audio URL into it, directly through waxum.", sessionScoped: true, danger: true,
			extraParams: []Param{{Name: "to", Type: "string", Desc: "Recipient.", Required: true}, {Name: "audio_url", Type: "string", Desc: "Publicly reachable audio URL.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.Str("to"), a.Str("audio_url")}, nil }},
		{name: "waxum_call_terminate", method: "Terminate", desc: "DANGER: terminate an in-progress outbound call, directly through waxum.", sessionScoped: true, danger: true,
			extraParams: []Param{{Name: "peer", Type: "string", Desc: "Call peer."}, {Name: "call_id", Type: "string", Desc: "Call id."}, {Name: "reason", Type: "string", Desc: "Termination reason."}},
			build:       func(a Args) ([]any, error) { return []any{a.Str("peer"), a.Str("call_id"), a.Str("reason")}, nil }},
		{name: "waxum_register_webhook", method: "RegisterWebhook", desc: "DANGER: register a webhook directly in waxum. The URL must not resolve to a private address (waxum's SSRF guard refuses it with no env escape hatch).", sessionScoped: true, danger: true,
			extraParams: []Param{{Name: "url", Type: "string", Desc: "Webhook URL.", Required: true}, {Name: "secret", Type: "string", Desc: "Optional shared secret."}, {Name: "events", Type: "array", Desc: "Event names to subscribe to.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.Str("url"), a.Str("secret"), a.StrSlice("events")}, nil }},
		{name: "waxum_list_webhooks", method: "ListWebhooks", desc: "List webhooks registered on a session, directly from waxum.", sessionScoped: true, readOnly: true, idempotent: true,
			build: noArgs},
		{name: "waxum_unregister_webhook", method: "UnregisterWebhook", desc: "DANGER: delete a registered webhook directly in waxum.", sessionScoped: true, danger: true,
			extraParams: []Param{{Name: "webhook_id", Type: "string", Desc: "Webhook id.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.Str("webhook_id")}, nil }},
		{name: "waxum_reenable_webhook", method: "ReenableWebhook", desc: "Re-enable a previously disabled webhook, directly through waxum.", sessionScoped: true,
			extraParams: []Param{{Name: "webhook_id", Type: "string", Desc: "Webhook id.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.Str("webhook_id")}, nil }},
		{name: "waxum_webhook_dlq", method: "WebhookDLQ", desc: "List a session's webhook dead-letter queue, directly from waxum.", sessionScoped: true, readOnly: true, idempotent: true,
			build: noArgs},
		{name: "waxum_replay_webhook_dlq", method: "ReplayWebhookDLQ", desc: "Replay one dead-lettered webhook delivery, directly through waxum.", sessionScoped: true,
			extraParams: []Param{{Name: "entry_id", Type: "string", Desc: "DLQ entry id.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.Str("entry_id")}, nil }},
		{name: "waxum_reenable_all_webhooks", method: "ReenableAllWebhooks", desc: "Re-enable every disabled webhook on this waxum instance (all sessions).",
			build: noArgs},
		{name: "waxum_upload_media", method: "UploadMedia", desc: "DANGER: upload a media file to waxum ahead of a send, returning a MediaDescriptor to pass to waxum_send_uploaded. file_base64 is decoded and uploaded as-is.", sessionScoped: true, danger: true,
			extraParams: []Param{
				{Name: "kind", Type: "string", Desc: "Media kind.", Required: true, Enum: []string{"image", "video", "audio", "document"}},
				{Name: "filename", Type: "string", Desc: "Filename.", Required: true}, {Name: "mimetype", Type: "string", Desc: "MIME type.", Required: true},
				{Name: "file_base64", Type: "string", Desc: "Base64-encoded file content.", Required: true},
			},
			build: func(a Args) ([]any, error) {
				raw, err := decodeBase64(a.Str("file_base64"))
				if err != nil {
					return nil, err
				}
				return []any{waxum.MediaKind(a.Str("kind")), a.Str("filename"), a.Str("mimetype"), raw}, nil
			}},
		{name: "waxum_download_media", method: "DownloadMedia", desc: "Download previously received/uploaded media from waxum, given a MediaDescriptor (as returned by e.g. waxum_upload_media). Returns raw bytes base64-encoded in the result.", sessionScoped: true, readOnly: true,
			extraParams: mediaDescriptorParams("Descriptor of the media to download."),
			build: func(a Args) ([]any, error) {
				d, err := mediaDescriptorFromArgs(a)
				if err != nil {
					return nil, err
				}
				return []any{d, waxum.MediaKind(a.Str("kind"))}, nil
			}},
		{name: "waxum_send_uploaded", method: "SendUploaded", desc: "DANGER: send a message using a MediaDescriptor already uploaded via waxum_upload_media, directly through waxum.", sessionScoped: true, danger: true,
			extraParams: append(mediaDescriptorParams("Descriptor from a prior waxum_upload_media call."),
				Param{Name: "to", Type: "string", Desc: "Recipient.", Required: true}, Param{Name: "caption", Type: "string", Desc: "Optional caption."},
				Param{Name: "filename", Type: "string", Desc: "Filename for documents."}, Param{Name: "ptt", Type: "boolean", Desc: "Send audio as a voice note."}),
			build: func(a Args) ([]any, error) {
				d, err := mediaDescriptorFromArgs(a)
				if err != nil {
					return nil, err
				}
				return []any{waxum.MediaKind(a.Str("kind")), a.Str("to"), d, a.Str("caption"), a.Str("filename"), a.Bool("ptt")}, nil
			}},
		{name: "waxum_session_messages", method: "SessionMessages", desc: "Paginate a session's inbound message log directly from waxum, newest first. Omit \"after\" for the newest page.", sessionScoped: true, readOnly: true, idempotent: true,
			extraParams: []Param{{Name: "after", Type: "integer", Desc: "waxum's own keyset cursor (its \"id\" column). Pages OLDER, not newer: pass the previous page's last row id to get the NEXT (older) page. Omit entirely (0 means \"omit\" here, not \"from the start\") to get the newest page."}, {Name: "limit", Type: "integer", Desc: "Max rows to return."}},
			build: func(a Args) ([]any, error) {
				var after *int64
				if n := a.Int("after", 0); n > 0 {
					v := int64(n)
					after = &v
				}
				return []any{after, a.Int("limit", 50)}, nil
			}},
		{name: "waxum_messages_search", method: "SearchMessages", desc: "Full-text search over a session's stored WhatsApp message history (DMs and groups, both directions), newest first. Hits carry chat_jid, sender_jid, direction, msg_type, body and a UTC received_at.", sessionScoped: true, readOnly: true, idempotent: true,
			extraParams: []Param{{Name: "q", Type: "string", Desc: "Search text (required, matched against message bodies/captions).", Required: true}, {Name: "limit", Type: "integer", Desc: "Page size (default 20, max 200)."}, {Name: "offset", Type: "integer", Desc: "Rows to skip (default 0)."}},
			build: func(a Args) ([]any, error) {
				if a.Str("q") == "" {
					return nil, fmt.Errorf("q must not be empty")
				}
				return []any{a.Str("q"), a.Int("limit", 20), a.Int("offset", 0)}, nil
			}},
		{name: "waxum_chat_messages", method: "ChatMessages", desc: "Read one chat's stored history (a DM partner JID or a group JID), newest first, with sender display names. Take chat_jid from a search hit, waxum_groups or waxum_contacts.", sessionScoped: true, readOnly: true, idempotent: true,
			extraParams: []Param{{Name: "chat_jid", Type: "string", Desc: "Chat JID, e.g. 62811@s.whatsapp.net or 1203...@g.us.", Required: true}, {Name: "limit", Type: "integer", Desc: "Page size (default 20, max 200)."}, {Name: "offset", Type: "integer", Desc: "Rows to skip (default 0)."}},
			build: func(a Args) ([]any, error) {
				if a.Str("chat_jid") == "" {
					return nil, fmt.Errorf("chat_jid must not be empty")
				}
				return []any{a.Str("chat_jid"), a.Int("limit", 20), a.Int("offset", 0)}, nil
			}},
		// "waxum_contacts" is NOT declared here — see waxumContactsTool()
		// (t_waxum_contacts.go): with q set, it is bespoke (combines this
		// session's waxum contacts with WSM's own owner contacts and
		// ranks both together via internal/contactmatch), so it cannot be
		// expressed as one waxumSpec.build call. WaxumMethod stays
		// "Contacts" so sync_test.go's TestEveryWaxumClientMethodHasAnMCPTool
		// still sees it covered.
		{name: "waxum_resolve_lid", method: "ResolveLID", desc: "Resolve the phone number behind a @lid sender/chat JID, directly through waxum.", sessionScoped: true, readOnly: true, idempotent: true,
			extraParams: []Param{{Name: "jid", Type: "string", Desc: "@lid JID to resolve.", Required: true}},
			build:       func(a Args) ([]any, error) { return []any{a.Str("jid")}, nil }},
	}
	out := make([]Tool, 0, len(specs)+2)
	for _, s := range specs {
		if s.method == "CreateSession" {
			out = append(out, createSessionTool())
			continue
		}
		out = append(out, s.tool())
	}
	out = append(out, waxumEventsTailTool())
	out = append(out, waxumContactsTool())
	return out
}

// createSessionTool is bespoke (not routed through waxumSpec.tool) because
// its first argument is the id to CREATE, not an id resolveWaxum looks up
// — there is nothing to resolve yet.
func createSessionTool() Tool {
	return Tool{
		Name: "waxum_create_session", WaxumMethod: "CreateSession", Danger: true,
		Desc: "DANGER: create a session directly in waxum (bypassing WSM's own session bookkeeping). Normally use wsm_sessions_create instead; this exists for direct recovery/diagnosis.",
		Params: []Param{
			{Name: "session_id", Type: "string", Desc: "Session id to create.", Required: true},
			{Name: "name", Type: "string", Desc: "Session display name.", Required: true},
			{Name: "reuse", Type: "boolean", Desc: "Reuse an existing on-disk store for this id if one exists."},
			targetParam,
		},
		Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
			id := a.Str("session_id")
			if id == "" {
				return nil, fmt.Errorf("session_id is required")
			}
			res, err := p.CallWaxum(ctx, "CreateSession", true, a.Str("target"), id, a.Str("name"), a.Bool("reuse"))
			if err != nil {
				return nil, err
			}
			return waxumResultOut(res), nil
		},
	}
}

func mediaDescriptorParams(desc string) []Param {
	return []Param{
		{Name: "kind", Type: "string", Desc: "Media kind.", Required: true, Enum: []string{"image", "video", "audio", "document"}},
		{Name: "url", Type: "string", Desc: desc + " url field."},
		{Name: "direct_path", Type: "string", Desc: desc + " direct_path field."},
		{Name: "media_key", Type: "string", Desc: desc + " media_key field."},
		{Name: "file_sha256", Type: "string", Desc: desc + " file_sha256 field."},
		{Name: "file_enc_sha256", Type: "string", Desc: desc + " file_enc_sha256 field."},
		{Name: "file_length", Type: "integer", Desc: desc + " file_length field."},
	}
}

func mediaDescriptorFromArgs(a Args) (waxum.MediaDescriptor, error) {
	return waxum.MediaDescriptor{
		URL: a.Str("url"), DirectPath: a.Str("direct_path"), MediaKey: a.Str("media_key"),
		FileSHA256: a.Str("file_sha256"), FileEncSHA256: a.Str("file_enc_sha256"),
		FileLength: int64(a.Int("file_length", 0)),
	}, nil
}
