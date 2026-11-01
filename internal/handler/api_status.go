package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// This file holds the three JSON routes wrapping the StatusPoster
// capability (internal/wa/capabilities.go) — posting a WhatsApp status/
// story through a session. /tmp/waxum-status-api.md is the spec these
// mirror; see that file's own first line for why the underlying waxum
// endpoints have NEVER been tried with a real send.
//
// Unlike every ordinary send in this app, a status post does NOT ride the
// per-session send queue (sender.go) — the capability is called directly
// and the result is known synchronously, so there is no 202-queued branch
// here the way postAPISendThrough has one. A disconnected session is NOT
// pre-checked client-side: exactly like checkContacts (api_contacts.go),
// the backend's own response is what decides that (waxum answers 503,
// mapErr turns it into wa.ErrNotConnected, writeWAError turns THAT into
// 409) — this file does not duplicate that check.
//
// Every post — success or failure — leaves exactly one wa_messages row via
// Manager.LogStatusPost, to_phone "status@broadcast", category manual, no
// schema change (CLAUDE.md decision). The row is written even when the
// post itself failed, so a failed status is still visible in the message
// log; see LogStatusPost's own doc comment.

// maxStatusRecipients mirrors maxVerifyPhones' reasoning (api_contacts.go):
// a generous but finite ceiling on one request's fan-out, not a batch-job
// limit.
const maxStatusRecipients = 1000

// parseStatusRecipients validates a caller-supplied recipient list: non-
// empty (waxum itself 400s on empty, but reporting that without a real
// network round trip is strictly better), each entry a parseable JID
// (wa.ParseJID — the UI sends wa_contacts' own stored jid, or
// wa.ToJID(phone) when a contact has none, per CLAUDE.md's decision), and
// under the cap above. Returns the recipients UNCHANGED (as JID strings,
// not wa.JID) — StatusPoster takes []string and passes them to waxum
// verbatim (capabilities.go's doc comment).
func parseStatusRecipients(raw []string) ([]string, string) {
	if len(raw) == 0 {
		return nil, "recipients is required and must not be empty"
	}
	if len(raw) > maxStatusRecipients {
		return nil, "too many recipients: max 1000 per request"
	}
	for _, r := range raw {
		if _, err := wa.ParseJID(strings.TrimSpace(r)); err != nil {
			return nil, "invalid recipient jid: " + r
		}
	}
	return raw, ""
}

// statusTextRequest is POST .../status/text's JSON body.
type statusTextRequest struct {
	Text           string   `json:"text"`
	BackgroundARGB uint32   `json:"background_argb"`
	Recipients     []string `json:"recipients"`
}

func decodeStatusTextRequest(w http.ResponseWriter, r *http.Request) (statusTextRequest, bool) {
	var req statusTextRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return statusTextRequest{}, false
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return statusTextRequest{}, false
	}
	recipients, msg := parseStatusRecipients(req.Recipients)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return statusTextRequest{}, false
	}
	req.Recipients = recipients
	return req, true
}

// parseStatusTextFormRequest is decodeStatusTextRequest's form-encoded
// sibling for the browser /status page (status_ui.go): the exact same
// validation (text required, recipients required/parseable/capped via
// parseStatusRecipients), but reading an ordinary POST form instead of a
// JSON body — status.html's recipient checkboxes all share the name
// "recipients" and arrive as repeated r.Form values, the same convention
// parseStatusMediaRequest accepts for its own "recipients" field.
func parseStatusTextFormRequest(r *http.Request) (statusTextRequest, string) {
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" {
		return statusTextRequest{}, "text is required"
	}
	var bg uint32
	if raw := strings.TrimSpace(r.FormValue("background_argb")); raw != "" {
		if n, err := strconv.ParseUint(raw, 10, 32); err == nil {
			bg = uint32(n)
		}
	}
	recipients, msg := parseStatusRecipients(r.Form["recipients"])
	if msg != "" {
		return statusTextRequest{}, msg
	}
	return statusTextRequest{Text: text, BackgroundARGB: bg, Recipients: recipients}, ""
}

// finishStatusPost is the shared tail every status route (JSON API and the
// browser /status page alike) calls right after the StatusPoster call
// itself returns: it logs exactly one wa_messages row via
// Manager.LogStatusPost, success or failure (CLAUDE.md's "log one
// wa_messages row per post" decision; see LogStatusPost's own doc
// comment), and returns that logging call's own error so each caller can
// decide how to report it in its own response shape (JSON vs an HTML
// partial) without duplicating the Insert/MarkSent/MarkFailed call itself.
func (h *handlers) finishStatusPost(ctx context.Context, s *wa.ManagedSession, body, mediaKind string, waMessageID string, postErr error) error {
	msg := &message.Message{
		ID: wa.NewID(), SessionID: s.ID, ToPhone: "status@broadcast",
		Body: body, Category: message.CategoryManual, MediaKind: mediaKind,
	}
	return h.Manager.LogStatusPost(ctx, msg, waMessageID, postErr)
}

// respondStatusPost is finishStatusPost's JSON-API wrapper — a logging
// failure is reported to the caller as a 500 only when the POST ITSELF
// succeeded, since a failed post with a failed log write has nothing
// useful left to report beyond the original error.
func (h *handlers) respondStatusPost(w http.ResponseWriter, ctx context.Context, s *wa.ManagedSession, body, mediaKind string, recipients []string, waMessageID string, postErr error) {
	logErr := h.finishStatusPost(ctx, s, body, mediaKind, waMessageID, postErr)
	if postErr != nil {
		writeWAError(w, postErr)
		return
	}
	if logErr != nil {
		writeError(w, http.StatusInternalServerError, "status posted but could not be logged: "+logErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message_id":      waMessageID,
		"session_id":      s.ID,
		"recipient_count": len(recipients),
	})
}

// PostAPISessionStatusText is POST /api/v1/sessions/{id}/status/text.
func (h *handlers) PostAPISessionStatusText(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	req, ok := decodeStatusTextRequest(w, r)
	if !ok {
		return
	}
	sp, err := h.Manager.StatusPoster()
	if err != nil {
		writeWAError(w, err)
		return
	}
	waID, postErr := sp.PostStatusText(r.Context(), s.ID, req.Text, req.BackgroundARGB, req.Recipients)
	h.respondStatusPost(w, r.Context(), s, req.Text, "", req.Recipients, waID, postErr)
}

// statusMediaRequest is the decoded multipart body shared by the image and
// video status routes — a narrower sibling of mediaSendRequest
// (api_media.go): no "to", no group routing, no ptt, since a status post
// has none of those.
type statusMediaRequest struct {
	Data        []byte
	Caption     string
	Recipients  []string
	Filename    string
	ContentType string
}

// parseStatusMediaRequest mirrors parseMediaSendRequest's steps for the
// fields a status post actually has: size cap, file, sniff, caption,
// recipients. "recipients" accepts BOTH shapes a caller might send it in:
// repeated form values (web/templates/status.html's recipient checkboxes —
// an ordinary HTML form posts one "recipients" value per checked box) and a
// single value that is itself a JSON array of JID strings (the JSON API /
// MCP convenience form, t_wsm_status.go). A single value that is neither a
// parseable JSON array nor empty is kept as-is — one plain JID is a valid
// one-element recipient list.
func (h *handlers) parseStatusMediaRequest(w http.ResponseWriter, r *http.Request, kind wa.MediaKind) (statusMediaRequest, *mediaDecodeError) {
	r.Body = http.MaxBytesReader(w, r.Body, h.MediaMaxBytes+mediaUploadSlack)

	err := r.ParseMultipartForm(mediaFormMaxMemory)
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err != nil {
		if isMaxBytesError(err) {
			return statusMediaRequest{}, &mediaDecodeError{http.StatusRequestEntityTooLarge, "upload exceeds the configured size limit"}
		}
		return statusMediaRequest{}, &mediaDecodeError{http.StatusBadRequest, "invalid multipart form"}
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		return statusMediaRequest{}, &mediaDecodeError{http.StatusBadRequest, "file is required"}
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return statusMediaRequest{}, &mediaDecodeError{http.StatusBadRequest, "could not read the uploaded file"}
	}

	if size, limit := int64(len(data)), h.mediaKindLimit(kind); size > limit {
		return statusMediaRequest{}, &mediaDecodeError{http.StatusRequestEntityTooLarge, h.mediaOversizeMessage(kind, size, limit)}
	}

	contentType, ok := sniffAndValidate(kind, data)
	if !ok {
		return statusMediaRequest{}, &mediaDecodeError{http.StatusUnsupportedMediaType,
			"uploaded file does not look like a " + string(kind) + " (sniffed " + contentType + ")"}
	}

	var recipients []string
	if r.MultipartForm != nil {
		recipients = append(recipients, r.MultipartForm.Value["recipients"]...)
	}
	if len(recipients) == 1 {
		var arr []string
		if err := json.Unmarshal([]byte(recipients[0]), &arr); err == nil {
			recipients = arr
		}
	}
	recipients, msg := parseStatusRecipients(recipients)
	if msg != "" {
		return statusMediaRequest{}, &mediaDecodeError{http.StatusBadRequest, msg}
	}

	return statusMediaRequest{
		Data: data, Caption: strings.TrimSpace(r.FormValue("caption")), Recipients: recipients,
		Filename: sanitizeFilename(header.Filename), ContentType: contentType,
	}, nil
}

// postStatusMedia is the shared orchestration tail for the image and video
// routes: upload-then-describe through the SAME path ordinary media sends
// use (uploadAndDescribeMedia — waxum's SSRF guard blocks a bare URL, see
// SendMedia's doc comment in internal/wa), then call the matching
// StatusPoster method.
func (h *handlers) postStatusMedia(w http.ResponseWriter, r *http.Request, kind wa.MediaKind) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	req, derr := h.parseStatusMediaRequest(w, r, kind)
	if derr != nil {
		writeError(w, derr.status, derr.msg)
		return
	}
	sp, err := h.Manager.StatusPoster()
	if err != nil {
		writeWAError(w, err)
		return
	}

	msgID := wa.NewID()
	media, err := h.uploadAndDescribeMedia(r.Context(), s, msgID, mediaSendRequest{
		Data: req.Data, Kind: kind, Caption: req.Caption, Filename: req.Filename, ContentType: req.ContentType,
	})
	if err != nil {
		writeWAError(w, err)
		return
	}

	var waID string
	var postErr error
	switch kind {
	case wa.MediaImage:
		waID, postErr = sp.PostStatusImage(r.Context(), s.ID, media.Outbound.Descriptor, req.Caption, req.Recipients)
	case wa.MediaVideo:
		waID, postErr = sp.PostStatusVideo(r.Context(), s.ID, media.Outbound.Descriptor, req.Caption, req.Recipients)
	}
	h.respondStatusPost(w, r.Context(), s, req.Caption, string(kind), req.Recipients, waID, postErr)
}

// PostAPISessionStatusImage is POST /api/v1/sessions/{id}/status/image.
func (h *handlers) PostAPISessionStatusImage(w http.ResponseWriter, r *http.Request) {
	h.postStatusMedia(w, r, wa.MediaImage)
}

// PostAPISessionStatusVideo is POST /api/v1/sessions/{id}/status/video.
func (h *handlers) PostAPISessionStatusVideo(w http.ResponseWriter, r *http.Request) {
	h.postStatusMedia(w, r, wa.MediaVideo)
}
