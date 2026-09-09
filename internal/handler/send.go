package handler

import (
	"context"
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
	// MediaLimits drives both the "Image 5 MB · Video 16 MB · …" summary
	// line and the "kind" select's per-option data-max-bytes attributes —
	// see mediaKindDisplays' doc comment for why this is built from
	// handlers.mediaKindLimit (api_media.go) rather than duplicated as
	// literal numbers here, which could silently drift from what the
	// server actually enforces.
	MediaLimits []mediaKindDisplay
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
	data := sendPageData{Connected: connected, MediaMaxBytes: h.MediaMaxBytes, MediaLimits: h.mediaKindDisplays()}
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
// the audit row, then hand it to wa.Manager.Dispatch (internal/wa/dispatch.go),
// which is dispatchSend's OLD inline body (Insert, Enqueue/EnqueueMedia, the
// 15s wait) moved down a layer so the self-convo runner (internal/selfconvo)
// can share the exact same audit-row-then-queue-then-wait path without an
// *http.Request to hang it off of — see Dispatch's doc comment for its full
// error contract. This function's own job shrank to what only an HTTP
// caller needs: building msg (media columns, the authenticated principal's
// APIClientID) and mapping Dispatch's outcome back to sendOutcome/status
// codes exactly as the old inline switch did — no observable behaviour
// changed, only where the work happens. media == nil selects the
// plain-text path; non-nil selects the media path and also fills the
// message row's five media columns.
func (h *handlers) dispatchSend(
	r *http.Request, s *wa.ManagedSession, msgID string, to wa.JID, toDisplay, text string,
	attr message.Attribution, media *mediaDispatch,
) (sendOutcome, int) {
	msg := &message.Message{ID: msgID, SessionID: s.ID, ToPhone: toDisplay,
		Body: text, Status: message.StatusQueued, CreatedAt: time.Now().UTC(),
		ClientRef: attr.ClientRef, TenantRef: attr.TenantRef, Category: attr.Category}
	var outboundMedia *wa.OutboundMedia
	if media != nil {
		msg.MediaKind = string(media.Outbound.Kind)
		msg.MediaFilename = media.Outbound.Filename
		msg.MediaMime = media.Mime
		msg.MediaSize = media.Size
		msg.MediaObjectKey = media.ObjectKey
		outboundMedia = &media.Outbound
	}
	// api_client_id comes from the authenticated principal, never the request body,
	// so a client cannot attribute its sends to another. Empty for the /send UI.
	if p, ok := auth.APIPrincipalFrom(r.Context()); ok {
		msg.APIClientID = p.ClientID
	}

	result, err := h.Manager.Dispatch(r.Context(), s, msg, to, outboundMedia, sendWaitLimit)
	if err != nil {
		if result.MsgID == "" {
			// Insert itself failed — Dispatch never reached Enqueue.
			return sendOutcome{Error: "could not store the message"}, http.StatusInternalServerError
		}
		return dispatchEnqueueOutcome(r.Context().Err(), result.MsgID, err)
	}
	if result.Result == nil {
		// The wait elapsed with no terminal result yet.
		return sendOutcome{ID: result.MsgID, Status: message.StatusQueued, Error: "still queued - it will be sent in order"}, http.StatusAccepted
	}
	if result.Result.Err != nil {
		return sendOutcome{ID: result.MsgID, Status: message.StatusFailed, Error: result.Result.Err.Error()}, http.StatusOK
	}
	return sendOutcome{ID: result.MsgID, Status: message.StatusSent, WAMessageID: result.Result.WAMessageID}, http.StatusOK
}

// dispatchEnqueueOutcome maps Manager.Dispatch's error contract (see its
// doc comment, internal/wa/dispatch.go) onto dispatchSend's HTTP response,
// for the case Dispatch already reached Enqueue (msgID is set — the row
// exists). Factored out of dispatchSend as its own pure function so the
// branch below CANNOT be exercised through a real Manager today (see its
// comment) is still directly testable — package handler has no way to
// fabricate a connected, sendable session (the same constraint
// internal/selfconvo/gateway.go's doc comment names for the runner), so an
// HTTP round trip can only ever reach the two enqueue sentinels below and
// the ctx-ended branch, never a hypothetical third enqueue error.
//
// reqCtxErr is r.Context().Err() at the point Dispatch returned — nil
// unless the caller's own request context had already ended.
func dispatchEnqueueOutcome(reqCtxErr error, msgID string, err error) (sendOutcome, int) {
	switch {
	case errors.Is(err, wa.ErrQueueFull):
		return sendOutcome{ID: msgID, Status: message.StatusFailed, Error: "send queue full"}, http.StatusTooManyRequests
	case errors.Is(err, wa.ErrNotConnected):
		return sendOutcome{ID: msgID, Status: message.StatusFailed, Error: "session not connected"}, http.StatusConflict
	case reqCtxErr != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		// The row was inserted and enqueued, but the request's own context
		// ended before a terminal result arrived — Dispatch's doc comment
		// names this the third branch. Same 202, no error text, as the old
		// select's <-r.Context().Done() case.
		return sendOutcome{ID: msgID, Status: message.StatusQueued}, http.StatusAccepted
	default:
		// Any OTHER enqueue error: Dispatch already marked the row failed
		// before returning it (see Dispatch's doc comment) — the response
		// must agree, not silently report 202 queued while the row is
		// actually failed. No enqueue error other than the two sentinels
		// above exists today (internal/wa/sender.go's enqueue, lines
		// 65-79's admission check has exactly two failure modes), so this
		// default is unreachable through the real Manager — it exists so a
		// future third enqueue-error sentinel fails SAFE (500, row and
		// response agree) instead of silently disagreeing with the DB.
		return sendOutcome{ID: msgID, Status: message.StatusFailed, Error: "could not queue the message"}, http.StatusInternalServerError
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
		// Always false: the UI's "Automatic routing" picker never opts into
		// a master session (unlike the API's allow_master flag) — an
		// operator who genuinely wants to send from a master session
		// selects it explicitly from the session dropdown instead, which IS
		// the direct-send path (session_id set, routing != "auto").
		s, _, ok = h.Manager.SelectConnected(r.Context(), false)
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
