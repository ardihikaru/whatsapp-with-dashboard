package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

const (
	maxTextChars  = 4096
	sendWaitLimit = 15 * time.Second
)

type sendPageData struct {
	PageBase
	Connected []wa.Snapshot
	// MediaMaxBytes feeds the media tab's data-max-bytes attribute — see
	// media-upload.js's doc comment on why the pre-check is client-side
	// AND best-effort (the server-side http.MaxBytesReader in
	// parseMediaSendRequest is the real limit; this only avoids uploading
	// a doomed file over a slow connection first).
	MediaMaxBytes int64
}

type sendResultData struct {
	Status      string // sent | queued | failed | "" (validation error)
	WAMessageID string
	Error       string
	SessionName string
	// Kind/Filename are set only by a media send (PostSendMedia) — "" for
	// the plain-text form, which send_result.html uses to decide whether
	// to mention the attachment at all.
	Kind     string
	Filename string
	// PresignedURL is set only when Deps.Objects archived the upload AND
	// could mint a presigned link back to it — best-effort on both counts
	// (archiveMedia and PresignedGet can each fail independently), so "" is
	// an ordinary outcome, not an error.
	PresignedURL string
}

// GetSend renders the compose page. Automatic routing is the default; the
// select allows explicit routing when needed.
func (h *handlers) GetSend(w http.ResponseWriter, r *http.Request) {
	var connected []wa.Snapshot
	for _, s := range h.Manager.Snapshots() {
		if s.Connected {
			connected = append(connected, s)
		}
	}
	data := sendPageData{Connected: connected, MediaMaxBytes: h.MediaMaxBytes}
	data.PageBase = h.newPageBase(r, "Send Message", "/send")
	h.Renderer.RenderPage(w, "send", data)
}

// PostSend validates, logs the message row, enqueues, and waits up to 15s for
// the worker. Every outcome — including validation errors — renders the
// send_result partial inline so the form contents are never lost.
func (h *handlers) PostSend(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" || len(text) > maxTextChars {
		h.sendResult(w, sendResultData{Error: "message text is required (max 4096 characters)"})
		return
	}
	digits, err := wa.NormalizePhone(r.FormValue("to"))
	if err != nil {
		h.sendResult(w, sendResultData{Error: err.Error()})
		return
	}
	s, errMsg := h.selectSendSession(r)
	if errMsg != "" {
		h.sendResult(w, sendResultData{Error: errMsg})
		return
	}

	// The human /send page is a manual, un-attributed send.
	attr := message.Attribution{Category: message.CategoryManual}
	outcome, statusCode := h.sendThroughSession(r, s, digits, text, attr)
	if statusCode >= 400 {
		h.sendResult(w, sendResultData{Status: outcome.Status, Error: outcome.Error, SessionName: s.Name})
		return
	}
	h.sendResult(w, sendResultData{
		Status: outcome.Status, WAMessageID: outcome.WAMessageID, Error: outcome.Error,
		SessionName: s.Name,
	})
}

func (h *handlers) sendResult(w http.ResponseWriter, data sendResultData) {
	h.Renderer.RenderPartial(w, "send_result", data)
}

type sendOutcome struct {
	ID          string
	Status      string
	WAMessageID string
	Error       string
}

// sendThroughSession is the phone-number path: the caller has bare digits.
func (h *handlers) sendThroughSession(r *http.Request, s *wa.ManagedSession, digits, text string, attr message.Attribution) (sendOutcome, int) {
	return h.sendThroughJID(r, s, wa.ToJID(digits), digits, text, attr)
}

// sendThroughJID is the whole TEXT send path — audit row, enqueue, 15s wait —
// for ANY destination. It took bare phone digits until Fase 9 and built the
// JID itself, which is what kept group sends out. Its exported behaviour
// (signature, status codes, outcome shape) is unchanged by the Phase 6a
// dispatch-seam refactor below: it mints its own message id and calls
// dispatchSend with no media, exactly what its old inline body did.
//
// Nothing below the queue needed changing: sendJob.to has always been a wa.JID
// and trySend hands it straight to the backend, so the token bucket, the retry
// loop and MarkSent are destination-agnostic already. That is why kb-30 is a
// validator plus a handler and touches no crypto.
func (h *handlers) sendThroughJID(
	r *http.Request, s *wa.ManagedSession, to wa.JID, toDisplay, text string, attr message.Attribution,
) (sendOutcome, int) {
	return h.dispatchSend(r, s, wa.NewID(), to, toDisplay, text, attr, nil)
}

// mediaDispatch is sendMediaThroughJID's media-send payload: the
// already-uploaded wa.OutboundMedia descriptor PLUS the wa_messages audit
// columns OutboundMedia itself does not carry (Mime/Size/ObjectKey — see
// message.Message's media columns doc comment). Kept as its own type,
// rather than widening OutboundMedia, because Mime/Size/ObjectKey are this
// app's own audit metadata and mean nothing to a backend's SendMedia call.
type mediaDispatch struct {
	Outbound  wa.OutboundMedia
	Mime      string
	Size      int64
	ObjectKey string
}

// sendMediaThroughJID is sendThroughJID's media sibling (Phase 6a's dispatch
// seam, §1b of the phase brief). msgID is minted by the CALLER — the media
// handler, before this is ever reached — because the MinIO object key that
// handler's best-effort archive step writes under embeds the message id
// (see storage.ObjectKey's doc comment): the id must exist before that
// upload, not after. media.Outbound must already carry a completed
// MediaStore.UploadMedia descriptor; this function and dispatchSend never
// upload, only Insert-then-enqueue, mirroring wa.EnqueueMedia's own
// "upload does not ride the queue" rule one layer down.
func (h *handlers) sendMediaThroughJID(
	r *http.Request, s *wa.ManagedSession, msgID string, to wa.JID, toDisplay string,
	attr message.Attribution, media mediaDispatch,
) (sendOutcome, int) {
	return h.dispatchSend(r, s, msgID, to, toDisplay, media.Outbound.Caption, attr, &media)
}

// dispatchSend is sendThroughJID/sendMediaThroughJID's shared tail: build
// the audit row, Insert it, Enqueue (or EnqueueMedia) it, and wait up to
// sendWaitLimit for a terminal result — the 15s wait, the
// ErrQueueFull/ErrNotConnected mapping and the MarkFailed-on-enqueue-failure
// fallback all live here exactly once, per Phase 6a §1b of the plan. media
// == nil selects the plain-text path (Enqueue); non-nil selects the media
// path (EnqueueMedia) and also fills the message row's five media columns.
func (h *handlers) dispatchSend(
	r *http.Request, s *wa.ManagedSession, msgID string, to wa.JID, toDisplay, text string,
	attr message.Attribution, media *mediaDispatch,
) (sendOutcome, int) {
	msg := &message.Message{ID: msgID, SessionID: s.ID, ToPhone: toDisplay,
		Body: text, Status: message.StatusQueued, CreatedAt: time.Now().UTC(),
		ClientRef: attr.ClientRef, TenantRef: attr.TenantRef, Category: attr.Category}
	if media != nil {
		msg.MediaKind = string(media.Outbound.Kind)
		msg.MediaFilename = media.Outbound.Filename
		msg.MediaMime = media.Mime
		msg.MediaSize = media.Size
		msg.MediaObjectKey = media.ObjectKey
	}
	// api_client_id comes from the authenticated principal, never the request body,
	// so a client cannot attribute its sends to another. Empty for the /send UI.
	if p, ok := auth.APIPrincipalFrom(r.Context()); ok {
		msg.APIClientID = p.ClientID
	}
	if err := h.Messages.Insert(r.Context(), msg); err != nil {
		return sendOutcome{Error: "could not store the message"}, http.StatusInternalServerError
	}

	var done <-chan wa.SendResult
	var err error
	if media != nil {
		done, err = s.EnqueueMedia(msg.ID, to, media.Outbound)
	} else {
		done, err = s.Enqueue(msg.ID, to, text)
	}
	if err != nil {
		reason := "could not queue the message"
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, wa.ErrQueueFull):
			reason = "send queue full"
			status = http.StatusTooManyRequests
		case errors.Is(err, wa.ErrNotConnected):
			reason = "session not connected"
			status = http.StatusConflict
		}
		_ = h.Messages.MarkFailed(r.Context(), msg.ID, reason, 0)
		return sendOutcome{ID: msg.ID, Status: message.StatusFailed, Error: reason}, status
	}

	select {
	case res := <-done:
		if res.Err != nil {
			return sendOutcome{ID: msg.ID, Status: message.StatusFailed, Error: res.Err.Error()}, http.StatusOK
		}
		return sendOutcome{ID: msg.ID, Status: message.StatusSent, WAMessageID: res.WAMessageID}, http.StatusOK
	case <-time.After(sendWaitLimit):
		return sendOutcome{ID: msg.ID, Status: message.StatusQueued, Error: "still queued - it will be sent in order"}, http.StatusAccepted
	case <-r.Context().Done():
		return sendOutcome{ID: msg.ID, Status: message.StatusQueued}, http.StatusAccepted
	}
}

// selectSendSession resolves the /send page's routing form fields
// ("routing", "session_id") to a connected session — shared by the text
// form (PostSend) and the media form (PostSendMedia, media_send_ui.go), so
// the two forms cannot silently drift on what "Automatic routing" means.
// Returns a human-readable error, never a wa.Err* sentinel: the browser
// forms render it straight into send_result's Error field.
func (h *handlers) selectSendSession(r *http.Request) (*wa.ManagedSession, string) {
	s, ok := h.Manager.Get(r.FormValue("session_id"))
	if r.FormValue("routing") == "auto" || r.FormValue("session_id") == "" {
		s, _, ok = h.Manager.SelectConnected(r.Context())
		if !ok {
			return nil, "no connected sessions available"
		}
		return s, ""
	}
	if !ok {
		return nil, "select a connected session"
	}
	return s, ""
}
