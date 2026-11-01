package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
)

// statusMediaTools wraps the two multipart status-post routes
// (POST /api/v1/sessions/{id}/status/image and .../status/video) — same
// reason mediaSendTools (t_wsm_media.go) is bespoke rather than routeSpec:
// WSM's own multipart contract (api_status.go) takes file_base64 plus a
// "recipients" FORM FIELD carrying a JSON array of JID strings, not a
// repeated form field — the MCP builds that string itself so a caller just
// passes a normal string array argument.
func statusMediaTools() []Tool {
	build := func(a Args) (map[string]string, []byte, error) {
		raw, err := base64.StdEncoding.DecodeString(a.Str("file_base64"))
		if err != nil {
			return nil, nil, fmt.Errorf("file_base64 did not decode: %w", err)
		}
		recipients := a.StrSlice("recipients")
		if len(recipients) == 0 {
			return nil, nil, fmt.Errorf("recipients is required and must not be empty")
		}
		recipientsJSON, err := json.Marshal(recipients)
		if err != nil {
			return nil, nil, err
		}
		fields := map[string]string{"caption": a.Str("caption"), "recipients": string(recipientsJSON)}
		return fields, raw, nil
	}
	params := []Param{
		{Name: "file_base64", Type: "string", Desc: "Base64-encoded file content.", Required: true},
		{Name: "caption", Type: "string", Desc: "Optional caption."},
		{Name: "recipients", Type: "array", Desc: "JIDs allowed to see this status, e.g. 559999999999@s.whatsapp.net. Required, non-empty.", Required: true},
	}
	image := Tool{
		Name: "wsm_session_status_image", Route: "POST /api/v1/sessions/{id}/status/image",
		Desc:   "Post a WhatsApp image status/story through this session (resolve by session_id or phone). Sent WITHOUT a thumbnail. Never tried against a real send until verified by hand (/tmp/waxum-status-api.md). Not Danger-gated. 501 on a backend with no StatusPoster support (e.g. whatsmeow).",
		Params: append([]Param{sessionIDParam, phoneParam, targetParam}, params...),
		Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
			fields, raw, err := build(a)
			if err != nil {
				return nil, err
			}
			h, id, err := p.ResolveSession(ctx, a.Str("session_id"), a.Str("phone"), a.Str("target"))
			if err != nil {
				return nil, err
			}
			res, err := p.CallWSMMultipart(ctx, "POST", "/api/v1/sessions/{id}/status/image", map[string]string{"id": id}, fields, "file", "status.jpg", raw, true, h.Name)
			if err != nil {
				return nil, err
			}
			return wsmResultOut(res), nil
		},
	}
	video := Tool{
		Name: "wsm_session_status_video", Route: "POST /api/v1/sessions/{id}/status/video",
		Desc:   "Post a WhatsApp video status/story through this session (resolve by session_id or phone). Sent WITHOUT a thumbnail. Never tried against a real send until verified by hand (/tmp/waxum-status-api.md). Not Danger-gated. 501 on a backend with no StatusPoster support (e.g. whatsmeow).",
		Params: append([]Param{sessionIDParam, phoneParam, targetParam}, params...),
		Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
			fields, raw, err := build(a)
			if err != nil {
				return nil, err
			}
			h, id, err := p.ResolveSession(ctx, a.Str("session_id"), a.Str("phone"), a.Str("target"))
			if err != nil {
				return nil, err
			}
			res, err := p.CallWSMMultipart(ctx, "POST", "/api/v1/sessions/{id}/status/video", map[string]string{"id": id}, fields, "file", "status.mp4", raw, true, h.Name)
			if err != nil {
				return nil, err
			}
			return wsmResultOut(res), nil
		},
	}
	return []Tool{image, video}
}
