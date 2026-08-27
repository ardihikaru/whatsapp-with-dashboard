package handler

import (
	"log"
	"net/http"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// decodeMediaSendRequestUI is parseMediaSendRequest's (api_media.go)
// HTML-partial wrapper: every failure renders straight into the
// send_result partial instead of a JSON body — the media-form counterpart
// of decodeMediaSendRequest. Both wrap the SAME parse so the two surfaces
// (JSON API, browser form) can never validate a multipart upload
// differently.
func (h *handlers) decodeMediaSendRequestUI(w http.ResponseWriter, r *http.Request) (mediaSendRequest, bool) {
	req, derr := h.parseMediaSendRequest(w, r)
	if derr != nil {
		h.sendResult(w, sendResultData{Error: derr.msg})
		return mediaSendRequest{}, false
	}
	return req, true
}

// PostSendMedia is POST /send/media — the browser form's media-attachment
// sibling of PostSend (send.go). Kept as its own handler, not folded into
// PostSend, for the same reason PostSend and PostAPISendMessage stay
// separate: a multipart decode and a urlencoded decode have nothing in
// common above dispatchSend's shared tail.
//
// Both hx-encoding="multipart/form-data" AND enctype="multipart/form-data"
// must be present on send.html's media form — the first is what htmx reads
// to send a real multipart body instead of urlencoded-in-a-string, the
// second is what a browser without JS uses for the identical
// action/method fallback. Shipping only one silently breaks whichever path
// nobody happened to test — see
// TestTheMediaFormCarriesBothHxEncodingAndEnctype (send_test.go).
func (h *handlers) PostSendMedia(w http.ResponseWriter, r *http.Request) {
	req, ok := h.decodeMediaSendRequestUI(w, r)
	if !ok {
		return
	}
	// The human /send page is a manual, un-attributed send — same rule as
	// PostSend's text path (send.go); the multipart form carries no
	// client_ref/tenant_ref/category fields for an operator to fill in.
	req.Attr = message.Attribution{Category: message.CategoryManual}

	s, errMsg := h.selectSendSession(r)
	if errMsg != "" {
		h.sendResult(w, sendResultData{Error: errMsg})
		return
	}

	msgID := wa.NewID()
	media, err := h.uploadAndDescribeMedia(r.Context(), s, msgID, req)
	if err != nil {
		h.sendResult(w, sendResultData{Error: err.Error(), SessionName: s.Name})
		return
	}

	outcome, _ := h.sendMediaThroughJID(r, s, msgID, wa.ToJID(req.Digits), req.Digits, req.Attr, media)

	result := sendResultData{
		Status: outcome.Status, WAMessageID: outcome.WAMessageID, Error: outcome.Error,
		SessionName: s.Name, Kind: string(req.Kind), Filename: req.Filename,
	}
	// Best-effort presigned link back to the archived original — see
	// archiveMedia's (api_media.go) doc comment on why a MinIO failure
	// never fails the send itself; the same rule applies here: no
	// PresignedURL is an ordinary outcome, not something to surface as an
	// error on an otherwise-successful send.
	if h.Objects != nil && media.ObjectKey != "" {
		if url, err := h.Objects.PresignedGet(r.Context(), media.ObjectKey); err == nil {
			result.PresignedURL = url
		} else {
			log.Printf("media send: could not mint a presigned link for %s (best-effort, continuing): %v", media.ObjectKey, err)
		}
	}
	h.sendResult(w, result)
}
