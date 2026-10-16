package registry

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
)

// mediaSendTools wraps the two multipart media-send routes
// (POST /api/v1/sessions/{id}/messages/media and POST /api/v1/messages/media).
// Both are multipart/form-data per openapi.yaml, not JSON, so they get a
// bespoke Tool rather than the generic routeSpec passthrough: the MCP
// takes file_base64+filename and builds the multipart body itself.
func mediaSendTools() []Tool {
	mediaParams := []Param{
		{Name: "to", Type: "string", Desc: "Recipient, international format (e.g. +62 812-0000-0000).", Required: true},
		{Name: "kind", Type: "string", Desc: "Media kind.", Required: true, Enum: []string{"image", "video", "audio", "document"}},
		{Name: "file_base64", Type: "string", Desc: "Base64-encoded file content.", Required: true},
		{Name: "filename", Type: "string", Desc: "Original filename, used for the multipart file part and (for documents) the recipient's view.", Required: true},
		{Name: "caption", Type: "string", Desc: "Optional caption."},
		{Name: "ptt", Type: "boolean", Desc: "Send audio as a push-to-talk voice note."},
		{Name: "client_ref", Type: "string", Desc: "Optional caller-supplied idempotency/reference tag."},
		{Name: "tenant_ref", Type: "string", Desc: "Optional tenant/customer reference tag."},
		{Name: "category", Type: "string", Desc: "Optional message category.", Enum: []string{"otp", "notification", "alert", "manual"}},
	}
	build := func(a Args) (map[string]string, []byte, error) {
		raw, err := base64.StdEncoding.DecodeString(a.Str("file_base64"))
		if err != nil {
			return nil, nil, fmt.Errorf("file_base64 did not decode: %w", err)
		}
		fields := map[string]string{
			"to": a.Str("to"), "kind": a.Str("kind"), "caption": a.Str("caption"),
			"client_ref": a.Str("client_ref"), "tenant_ref": a.Str("tenant_ref"), "category": a.Str("category"),
		}
		if a.Bool("ptt") {
			fields["ptt"] = "true"
		}
		return fields, raw, nil
	}
	sessionSend := Tool{
		Name: "wsm_session_send_media", Route: "POST /api/v1/sessions/{id}/messages/media",
		Desc:   "Send an image, video, audio or document message through one explicit session (resolve by session_id or phone). Builds the multipart/form-data body from file_base64. Not Danger-gated: goes through WSM's own auth/routing/queue/log like every other send.",
		Params: append([]Param{sessionIDParam, phoneParam, targetParam}, mediaParams...),
		Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
			fields, raw, err := build(a)
			if err != nil {
				return nil, err
			}
			h, id, err := p.ResolveSession(ctx, a.Str("session_id"), a.Str("phone"), a.Str("target"))
			if err != nil {
				return nil, err
			}
			res, err := p.CallWSMMultipart(ctx, "POST", "/api/v1/sessions/{id}/messages/media", map[string]string{"id": id}, fields, "file", a.Str("filename"), raw, true, h.Name)
			if err != nil {
				return nil, err
			}
			return wsmResultOut(res), nil
		},
	}
	auto := Tool{
		Name: "wsm_send_media_auto", Route: "POST /api/v1/messages/media",
		Desc:   "Send an image, video, audio or document message using automatic session routing (no session_id needed). Builds the multipart/form-data body from file_base64. Not Danger-gated.",
		Params: append([]Param{targetParam}, mediaParams...),
		Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
			fields, raw, err := build(a)
			if err != nil {
				return nil, err
			}
			res, err := p.CallWSMMultipart(ctx, "POST", "/api/v1/messages/media", nil, fields, "file", a.Str("filename"), raw, true, a.Str("target"))
			if err != nil {
				return nil, err
			}
			return wsmResultOut(res), nil
		},
	}
	return []Tool{sessionSend, auto}
}
