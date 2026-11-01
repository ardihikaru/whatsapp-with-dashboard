package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/storage"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// mediaUploadSlack is headroom over Deps.MediaMaxBytes that
// http.MaxBytesReader allows for the multipart ENVELOPE around the file
// itself (boundaries, per-part headers, the other form fields) — a file
// exactly at the configured limit must not be rejected by the wrapper
// around it. maxBodyBytes (api.go) is the unrelated JSON cap and is left
// untouched, per the phase brief.
const mediaUploadSlack = 64 * 1024

// mediaFormMaxMemory is ParseMultipartForm's in-RAM ceiling: at most this
// much of the request is buffered in memory, the rest spills to a temp file
// under os.TempDir(). decodeMediaSendRequest's deferred
// r.MultipartForm.RemoveAll() is the only thing that deletes that spill
// file — see TestTheMultipartSpillFileIsRemovedAfterTheRequest.
const mediaFormMaxMemory = 1 << 20 // 1 MiB

// mediaAllowedContentTypes is the sniffed-content-type allowlist for the
// two kinds http.DetectContentType covers cleanly. audio and document have
// their own checks in sniffAndValidate — see that function's doc comment
// for the audio detail, and for a place this file's original spec did not
// match what was actually observed on this Go toolchain. video/webm is here
// because http.DetectContentType matches WebM's EBML magic
// (\x1A\x45\xDF\xA3) unconditionally — unlike MP4, there is no brand
// scanning to fail — verified directly against this repo's Go toolchain
// (go1.26.7's sniff.go, exactSig on that magic).
var mediaAllowedContentTypes = map[wa.MediaKind]map[string]bool{
	wa.MediaImage: {"image/jpeg": true, "image/png": true, "image/webp": true},
	wa.MediaVideo: {"video/mp4": true, "video/webm": true},
}

// parseMediaKind validates the "kind" form field against the four values
// wa.MediaKind actually supports, rather than accepting any string a caller
// sends (which would reach waxum as a bogus media_type form field on
// UploadMedia).
func parseMediaKind(v string) (wa.MediaKind, bool) {
	switch wa.MediaKind(v) {
	case wa.MediaImage, wa.MediaVideo, wa.MediaAudio, wa.MediaDocument:
		return wa.MediaKind(v), true
	default:
		return "", false
	}
}

// mediaKindLimit resolves the per-kind size ceiling a decoded upload of the
// given kind must not exceed: Deps.MediaMaxBytes{Image,Video,Audio,Document}
// (internal/config/media.go's loadMedia — WhatsApp's own published limits
// differ per kind, so one global number either under-restricts a document
// or over-permits a video past what WhatsApp will accept). Falls back to
// Deps.MediaMaxBytes when the per-kind field is unset (<=0) — the same
// "zero-value Deps must not deadlock/reject everything" rule
// mediaUploadConcurrency (routes.go) already follows, needed because most
// of this package's handler tests build a Deps literal that predates these
// fields.
func (h *handlers) mediaKindLimit(kind wa.MediaKind) int64 {
	var limit int64
	switch kind {
	case wa.MediaImage:
		limit = h.MediaMaxBytesImage
	case wa.MediaVideo:
		limit = h.MediaMaxBytesVideo
	case wa.MediaAudio:
		limit = h.MediaMaxBytesAudio
	case wa.MediaDocument:
		limit = h.MediaMaxBytesDocument
	}
	if limit <= 0 || limit > h.MediaMaxBytes {
		limit = h.MediaMaxBytes
	}
	return limit
}

// mediaKindDisplayOrder is the four kinds in the order both the "kind"
// <select> and the limits summary line show them on /send — image, video,
// audio, document, matching send.html's original hand-written option
// order.
var mediaKindDisplayOrder = []struct {
	kind  wa.MediaKind
	label string
}{
	{wa.MediaImage, "Image"},
	{wa.MediaVideo, "Video"},
	{wa.MediaAudio, "Audio"},
	{wa.MediaDocument, "Document"},
}

// mediaKindDisplay is one row of /send's per-kind size-limit summary and
// one <option> of its "kind" select — see sendPageData.MediaLimits.
type mediaKindDisplay struct {
	Kind  string // "image" — the <option value> and data-media-limit key
	Label string // "Image"
	Bytes int64
	Human string // "5.0 MB" — formatMediaBytes(Bytes)
	// Accept is the comma-separated <input accept> list for this kind —
	// see mediaKindAccept. "" means "any file" (document).
	Accept string
}

// mediaAudioContentTypes and mediaVideoExtraContentTypes name the types
// sniffAndValidate accepts through its own magic-number checks rather than
// through mediaAllowedContentTypes (audio bypasses the map entirely; video
// adds 3GPP and QuickTime on top of it). They exist so mediaKindAccept can
// offer exactly what the server takes; keep them in step with the cases in
// sniffAndValidate.
var (
	mediaAudioContentTypes      = []string{"audio/ogg", "audio/mpeg", "audio/mp4"}
	mediaVideoExtraContentTypes = []string{"video/3gpp", "video/quicktime"}
)

// mediaKindAccept builds the <input accept> list for a kind from the
// server's own allowlist, so the browser picker and sniffAndValidate cannot
// drift. Document returns "": the server accepts any document, and an
// accept list would hide files it takes.
func mediaKindAccept(kind wa.MediaKind) string {
	var types []string
	for ct := range mediaAllowedContentTypes[kind] {
		types = append(types, ct)
	}
	sort.Strings(types)
	switch kind {
	case wa.MediaVideo:
		types = append(types, mediaVideoExtraContentTypes...)
	case wa.MediaAudio:
		types = append(types, mediaAudioContentTypes...)
	}
	return strings.Join(types, ",")
}

// mediaKindDisplays builds send.html's MediaLimits straight from
// mediaKindLimit — the SAME function parseMediaSendRequest's size check
// calls — so the number shown to the operator can never drift from the
// number the server actually enforces. See
// TestMediaKindDisplaysReadsFromDepsConfigNotAConstant and
// TestSendPageRendersEveryKindsConfiguredLimit (media_send_ui_test.go),
// which together pin exactly that: the limits come from Deps config, not
// literals baked into this function or into send.html.
func (h *handlers) mediaKindDisplays() []mediaKindDisplay {
	out := make([]mediaKindDisplay, 0, len(mediaKindDisplayOrder))
	for _, k := range mediaKindDisplayOrder {
		limit := h.mediaKindLimit(k.kind)
		out = append(out, mediaKindDisplay{
			Kind: string(k.kind), Label: k.label, Bytes: limit, Human: formatMediaBytes(limit),
			Accept: mediaKindAccept(k.kind),
		})
	}
	return out
}

// formatMediaBytes renders a byte count the way an operator reads it —
// mirrors media-upload.js's own formatBytes exactly, so the client-side
// pre-check and this server-side rejection never disagree about what "16
// MB" means.
func formatMediaBytes(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// mediaOversizeMessage builds parseMediaSendRequest's per-kind rejection
// text: the kind, the actual size and that kind's limit, always — plus a
// concrete next step. A video/audio/image too large for its own kind may
// still fit as a document (the document limit is typically far higher, see
// DefaultMediaMaxBytesDocument), so that suggestion is only made when
// THIS DEPLOYMENT'S configured document limit is actually bigger than the
// limit that was just missed — never assumed.
func (h *handlers) mediaOversizeMessage(kind wa.MediaKind, size, limit int64) string {
	msg := fmt.Sprintf("%s upload is %s, over the %s limit configured for %s on this deployment.",
		kind, formatMediaBytes(size), formatMediaBytes(limit), kind)
	if kind == wa.MediaDocument {
		return msg + " Compress it before sending."
	}
	if docLimit := h.mediaKindLimit(wa.MediaDocument); docLimit > limit {
		return msg + fmt.Sprintf(" Compress it, or send it as a document instead (document limit %s on this deployment) if that fits your use case.", formatMediaBytes(docLimit))
	}
	return msg + " Compress it before sending."
}

// hasFtypBox reports whether data opens with an ISO-base-media "ftyp" box
// header (offset 4, 4 bytes) — the container format shared by MP4, M4A/AAC,
// and 3GPP files alike.
func hasFtypBox(data []byte) bool {
	return len(data) >= 8 && bytes.Equal(data[4:8], []byte("ftyp"))
}

// has3GPPBrand reports whether an ftyp box's major brand (bytes 8:12) is a
// 3GPP family brand ("3gp4", "3g2a", …). http.DetectContentType's own
// mp4Sig only recognizes a brand that itself STARTS WITH "mp4" among the
// box's compatible-brand slots (see the stdlib's sniff.go) — a real 3GPP
// file whose brands are all "3gp*"/"3g2*" and never "mp4*" sniffs as
// application/octet-stream there, so the video kind's allowlist needs this
// as a second check alongside mediaAllowedContentTypes.
func has3GPPBrand(data []byte) bool {
	if !hasFtypBox(data) || len(data) < 12 {
		return false
	}
	brand := string(data[8:12])
	return strings.HasPrefix(brand, "3gp") || strings.HasPrefix(brand, "3g2")
}

// isQuickTimeBrand reports whether an ftyp box's major brand (bytes 8:12) is
// QuickTime's own "qt  " (note the trailing spaces — it is padded to 4
// bytes, not a typo). http.DetectContentType's mp4Sig never recognizes this
// brand as "mp4" (see the stdlib's sniff.go: it only matches a
// compatible-brand slot that ITSELF starts with "mp4"), so an iPhone .mov
// sniffs as application/octet-stream and used to be rejected outright —
// measured directly against this repo's Go toolchain.
func isQuickTimeBrand(data []byte) bool {
	if !hasFtypBox(data) || len(data) < 12 {
		return false
	}
	return string(data[8:12]) == "qt  "
}

// sniffAndValidate confirms the UPLOADED BYTES — never the multipart part's
// declared Content-Type header, which a client fully controls — actually
// look like the declared kind, returning the sniffed content type.
//
// A NOTE ON WHAT WAS ACTUALLY OBSERVED (this repo's evidence-not-opinion
// rule): this feature was originally specified assuming
// http.DetectContentType returns "application/octet-stream" for OGG/Opus.
// Measured directly against this repo's Go toolchain
// (`go run` a small probe against http.DetectContentType with real "OggS"
// bytes), that is NOT what happens — it returns "application/ogg" cleanly,
// because the stdlib's OggS signature match predates this feature. What IS
// true, and IS why audio needs its own magic-number check rather than
// relying on DetectContentType alone: an MP4-container audio file (M4A/AAC)
// sniffs as "video/mp4" — the exact same result a real video gets — and a
// raw MP3 (ID3-tagged) sniffs as "audio/mpeg", which DOES need accepting
// for the audio kind but is never in mediaAllowedContentTypes (that map is
// image/video only). So audio bypasses the map entirely and checks the
// three magic numbers directly.
func sniffAndValidate(kind wa.MediaKind, data []byte) (contentType string, ok bool) {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	sniffed := http.DetectContentType(data)
	switch kind {
	case wa.MediaDocument:
		// waxum's own document endpoint validates no mimetype at all — see
		// the phase brief's allowlist ("document → anything").
		return sniffed, true
	case wa.MediaImage:
		return sniffed, mediaAllowedContentTypes[kind][sniffed]
	case wa.MediaVideo:
		if mediaAllowedContentTypes[kind][sniffed] {
			return sniffed, true
		}
		if has3GPPBrand(head) {
			return "video/3gpp", true
		}
		// Any other ISO-BMFF ftyp container is accepted, mirroring what
		// audio already does for the same container family (hasFtypBox
		// above, and this function's doc comment on why audio bypasses
		// mediaAllowedContentTypes entirely). Without this, http.DetectContentType's
		// mp4Sig rejects any real MP4 whose compatible-brand list never
		// contains a brand literally starting with "mp4" — measured
		// directly: isom+[isom iso2 avc1] (no explicit mp4 brand) and
		// isom+[isom iso2 hvc1] (HEVC) both sniff as
		// application/octet-stream today, despite being ordinary MP4 video
		// that audio's own ftyp fallback would already accept as audio/mp4.
		if hasFtypBox(head) {
			// QuickTime's own "qt  " brand is labelled honestly as
			// video/quicktime rather than silently as video/mp4: a .mov is
			// a different (superset) container from MP4 and WhatsApp's
			// documented video mimetypes are video/mp4 and video/3gpp only,
			// not video/quicktime. Mislabelling it "video/mp4" would let it
			// past this check only to fail somewhere downstream (waxum or
			// WhatsApp itself) with a confusing error that doesn't mention
			// the real container. Labelling it accurately means a rejection,
			// if there is one, happens with an honest mimetype in the
			// message instead.
			if isQuickTimeBrand(head) {
				return "video/quicktime", true
			}
			return "video/mp4", true
		}
		return sniffed, false
	case wa.MediaAudio:
		switch {
		case bytes.HasPrefix(head, []byte("OggS")):
			return "audio/ogg", true
		case bytes.HasPrefix(head, []byte("ID3")):
			return "audio/mpeg", true
		case hasFtypBox(head):
			return "audio/mp4", true
		default:
			return sniffed, false
		}
	default:
		return sniffed, false
	}
}

// sanitizeFilename strips any directory component and control characters
// from an operator-supplied filename and caps it at 255 bytes, defaulting
// to "file" for anything that sanitizes down to empty — mirrors the phase
// brief's step 7 exactly.
func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	name = b.String()
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "file"
	}
	if len(name) > 255 {
		name = name[:255]
	}
	return name
}

func parseFormBool(v string) bool {
	b, _ := strconv.ParseBool(v)
	return b
}

// mediaSendRequest is decodeMediaSendRequest's parsed, validated result —
// everything sendMedia needs to archive, upload and dispatch.
//
// AllowMaster governs automatic routing only (PostAPISendMediaAuto); the
// explicit-session route ignores it. Unlike every other allow_master field
// in this package, it arrives as a MULTIPART FORM FIELD ("allow_master"),
// not a JSON body key — this endpoint is multipart end to end (see
// parseMediaSendRequest below) and has no JSON body to add a key to.
type mediaSendRequest struct {
	Data    []byte
	Kind    wa.MediaKind
	Digits  string
	Caption string
	PTT     bool
	// Route carries allow_master/group_code, folded together like every
	// other send request — see textSendRequest.Route. MasterOnly is always
	// false: this endpoint was not asked to gain master_only, and the
	// multipart form is never even read for a "master_only" field (see
	// parseMediaSendRequest and PostAPISendMediaAuto's own comments).
	Route       wa.Route
	Filename    string
	ContentType string
	Attr        message.Attribution
}

// isMaxBytesError reports whether err is (or wraps) the error
// http.MaxBytesReader produces once its limit is exceeded.
func isMaxBytesError(err error) bool {
	var mbErr *http.MaxBytesError
	return errors.As(err, &mbErr)
}

// mediaDecodeError is parseMediaSendRequest's typed failure: the HTTP
// status an API caller should see PLUS the message. Kept as its own type,
// rather than writing straight to a ResponseWriter, so the UI's
// /send/media handler (media_send_ui.go) can render the SAME validation
// failure into the send_result partial instead of a JSON body — the same
// decode-then-format split dispatchSend/sendThroughJID already use for the
// text path (send.go).
type mediaDecodeError struct {
	status int
	msg    string
}

func (e *mediaDecodeError) Error() string { return e.msg }

// parseMediaSendRequest is the multipart half of the media-send handlers:
// steps 1-7 of the phase brief. Every media-send route — explicit session
// id, auto-routed, or the UI's own /send/media — shares it. Writes nothing
// to w itself beyond what http.MaxBytesReader needs to enforce its limit;
// see decodeMediaSendRequest (the JSON-response wrapper) and
// decodeMediaSendRequestUI (media_send_ui.go, the HTML-partial wrapper).
func (h *handlers) parseMediaSendRequest(w http.ResponseWriter, r *http.Request) (mediaSendRequest, *mediaDecodeError) {
	// Step 1: refuse an oversized upload at the socket, before
	// ParseMultipartForm ever buffers it.
	r.Body = http.MaxBytesReader(w, r.Body, h.MediaMaxBytes+mediaUploadSlack)

	// Step 2-3: ≤1 MiB in RAM, the rest spills to a temp file — and that
	// spill file is the ONLY thing r.MultipartForm.RemoveAll() below
	// exists to delete. Deferred unconditionally on any non-nil
	// MultipartForm, including the error path, because a MaxBytesReader
	// failure can still leave a partially-written spill file behind.
	err := r.ParseMultipartForm(mediaFormMaxMemory)
	if r.MultipartForm != nil {
		defer func() {
			if rmErr := r.MultipartForm.RemoveAll(); rmErr != nil {
				log.Printf("media upload: could not remove multipart spill file(s): %v", rmErr)
			}
		}()
	}
	if err != nil {
		if isMaxBytesError(err) {
			// Distinguishable from the sniff-failure log below: this is a
			// SIZE rejection (never got to read "kind" or the file bytes),
			// not a content-mismatch one. Never logs the body itself.
			log.Printf("media upload: rejected, exceeds the configured limit (%d bytes + %d slack)", h.MediaMaxBytes, mediaUploadSlack)
			return mediaSendRequest{}, &mediaDecodeError{http.StatusRequestEntityTooLarge, "upload exceeds the configured size limit"}
		}
		return mediaSendRequest{}, &mediaDecodeError{http.StatusBadRequest, "invalid multipart form"}
	}

	// Step 4.
	file, header, err := r.FormFile("file")
	if err != nil {
		return mediaSendRequest{}, &mediaDecodeError{http.StatusBadRequest, "file is required"}
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return mediaSendRequest{}, &mediaDecodeError{http.StatusBadRequest, "could not read the uploaded file"}
	}

	kind, ok := parseMediaKind(r.FormValue("kind"))
	if !ok {
		return mediaSendRequest{}, &mediaDecodeError{http.StatusBadRequest, "kind must be one of image, video, audio, document"}
	}

	// Step 5b: per-kind size check, ahead of the sniff below. Placed right
	// after "kind" resolves — the earliest point that limit is knowable —
	// rather than at the very top: the socket-level MaxBytesReader check
	// (step 1) is the one check that MUST run before the kind is even
	// read, but this one wants the kind, and running it before the
	// (comparatively expensive, on a large file) sniff below means a file
	// that is oversized for its kind never pays for a sniff it cannot use.
	if size, limit := int64(len(data)), h.mediaKindLimit(kind); size > limit {
		log.Printf("media upload: rejected as %s, %d bytes exceeds the %d byte limit for that kind", kind, size, limit)
		return mediaSendRequest{}, &mediaDecodeError{http.StatusRequestEntityTooLarge, h.mediaOversizeMessage(kind, size, limit)}
	}

	digits, err := wa.NormalizePhone(r.FormValue("to"))
	if err != nil {
		return mediaSendRequest{}, &mediaDecodeError{http.StatusBadRequest, err.Error()}
	}

	// group_code arrives as a multipart FORM FIELD, like allow_master,
	// because this endpoint has no JSON body. Never reads a "master_only"
	// form field — see mediaSendRequest.Route's doc comment.
	groupCode, err := wa.NormalizeGroupCode(r.FormValue("group_code"))
	if err != nil {
		return mediaSendRequest{}, &mediaDecodeError{http.StatusBadRequest, err.Error()}
	}

	// Step 6: sniff, never trust the part's declared Content-Type.
	contentType, ok := sniffAndValidate(kind, data)
	if !ok {
		// This is what caught the original video-send bug: before this log
		// line existed, a rejection here was invisible — no wa_messages
		// row (Insert happens well after this), no log line, and the HTTP
		// response is a plain 200 to the UI form (media_send_ui.go renders
		// the failure into a partial, not a status code). Never logs the
		// file bytes themselves.
		log.Printf("media upload: rejected as %s (sniffed %s, %d bytes)", kind, contentType, len(data))
		return mediaSendRequest{}, &mediaDecodeError{http.StatusUnsupportedMediaType,
			"uploaded file does not look like a " + string(kind) + " (sniffed " + contentType + ")"}
	}

	return mediaSendRequest{
		Data:    data,
		Kind:    kind,
		Digits:  digits,
		Caption: strings.TrimSpace(r.FormValue("caption")),
		PTT:     parseFormBool(r.FormValue("ptt")),
		// MasterOnly hardcoded false: see mediaSendRequest.Route's doc comment.
		Route:       wa.Route{GroupCode: groupCode, AllowMaster: parseFormBool(r.FormValue("allow_master")), MasterOnly: false},
		Filename:    sanitizeFilename(header.Filename), // step 7
		ContentType: contentType,
		Attr: message.Attribution{
			ClientRef: strings.TrimSpace(r.FormValue("client_ref")),
			TenantRef: strings.TrimSpace(r.FormValue("tenant_ref")),
			Category:  message.NormalizeCategory(r.FormValue("category"), message.CategoryNotification),
		},
	}, nil
}

// decodeMediaSendRequest is parseMediaSendRequest's JSON-API wrapper: every
// failure becomes writeError's usual {"error": "..."} body at the status
// parseMediaSendRequest chose.
func (h *handlers) decodeMediaSendRequest(w http.ResponseWriter, r *http.Request) (mediaSendRequest, bool) {
	req, derr := h.parseMediaSendRequest(w, r)
	if derr != nil {
		writeError(w, derr.status, derr.msg)
		return mediaSendRequest{}, false
	}
	return req, true
}

// PostAPISessionSendMedia is POST /api/v1/sessions/{id}/messages/media —
// the explicit-session media send.
func (h *handlers) PostAPISessionSendMedia(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	req, ok := h.decodeMediaSendRequest(w, r)
	if !ok {
		return
	}
	h.sendMedia(w, r, s, req, routingExplicit)
}

// PostAPISendMediaAuto is POST /api/v1/messages/media — auto-routed,
// mirroring PostAPISendMessageAuto's shape: decode first (the body is the
// expensive part here), THEN pick a connected session. Route.MasterOnly is
// always false — hardcoded in parseMediaSendRequest — because this endpoint
// was NOT asked to gain master_only and its wire contract stays unchanged;
// do not add a "master_only" form field here.
func (h *handlers) PostAPISendMediaAuto(w http.ResponseWriter, r *http.Request) {
	req, ok := h.decodeMediaSendRequest(w, r)
	if !ok {
		return
	}
	s, _, ok := selectForSend(r, h.Manager, req.Route)
	if !ok {
		writeError(w, http.StatusConflict, noSendRouteMessage(h.Manager.Snapshots(), req.Route))
		return
	}
	h.sendMedia(w, r, s, req, routingModeFor(req.Route))
}

// archiveMedia writes the uploaded bytes to MinIO BEFORE the backend
// upload. BEST-EFFORT: a failure here is logged and rung against the
// session but never fails the send — waxum holds the canonical copy once
// UploadMedia below succeeds, and making MinIO a hard dependency of every
// media send would be the worse availability trade (Phase 6a plan, §3
// step 8). Returns "" on a skip (Deps.Objects unset) or a failure, which
// dispatchSend then persists as a NULL media_object_key
// (message.nullIfEmpty).
func (h *handlers) archiveMedia(ctx context.Context, s *wa.ManagedSession, msgID string, req mediaSendRequest) string {
	if h.Objects == nil {
		return ""
	}
	key := storage.ObjectKey(s.ID, msgID, time.Now().UTC(), filepath.Ext(req.Filename))
	if err := h.Objects.Put(ctx, key, bytes.NewReader(req.Data), int64(len(req.Data)), req.ContentType); err != nil {
		log.Printf("media archive: put %s failed (best-effort, continuing): %v", key, err)
		s.RingWarn("media archive to object storage failed: " + err.Error())
		return ""
	}
	return key
}

// uploadAndDescribeMedia is the archive-then-upload half of the media-send
// orchestration (steps 8-10 of the phase brief), factored out of sendMedia
// so the UI's own /send/media handler (media_send_ui.go) can share it and
// diverge only in how it formats the final response — the same
// decode/dispatch split sendThroughJID/sendMediaThroughJID already use for
// the text path. msgID is minted by the caller, BEFORE the archive: its
// key embeds this id.
func (h *handlers) uploadAndDescribeMedia(ctx context.Context, s *wa.ManagedSession, msgID string, req mediaSendRequest) (mediaDispatch, error) {
	var objectKey string
	if h.MediaArchive {
		objectKey = h.archiveMedia(ctx, s, msgID, req)
	}

	// Semaphore bounds concurrent UPLOADS (which happen here, before
	// enqueue) — sendQueueCap bounds the queue, a different resource. See
	// handlers.mediaSem's doc comment (routes.go).
	h.mediaSem <- struct{}{}
	defer func() { <-h.mediaSem }()

	store, err := h.Manager.MediaStore()
	if err != nil {
		return mediaDispatch{}, err
	}
	uctx, cancel := context.WithTimeout(ctx, h.MediaUploadTimeout)
	descriptor, err := store.UploadMedia(uctx, s.ID, req.Kind, req.Filename, req.ContentType, req.Data)
	cancel()
	if err != nil {
		return mediaDispatch{}, err
	}

	return mediaDispatch{
		Outbound: wa.OutboundMedia{
			Kind: req.Kind, Descriptor: descriptor, Caption: req.Caption,
			Filename: req.Filename, PTT: req.PTT,
		},
		Mime: req.ContentType, Size: int64(len(req.Data)), ObjectKey: objectKey,
	}, nil
}

// sendMedia is the shared orchestration tail for both JSON media-send
// routes: mint the id, archive+upload via uploadAndDescribeMedia, then
// dispatch through sendMediaThroughJID — steps 8-11 of the phase brief. mode
// reports the routing decision, matching postAPISendThrough's own
// routingExplicit/routingModeFor split.
func (h *handlers) sendMedia(w http.ResponseWriter, r *http.Request, s *wa.ManagedSession, req mediaSendRequest, mode routingMode) {
	msgID := wa.NewID()
	media, err := h.uploadAndDescribeMedia(r.Context(), s, msgID, req)
	if err != nil {
		writeWAError(w, err)
		return
	}

	outcome, status := h.sendMediaThroughJID(r, s, msgID, wa.ToJID(req.Digits), req.Digits, req.Attr, media)
	if status >= 400 {
		writeError(w, status, outcome.Error)
		return
	}
	snap := s.Snapshot()
	resp := map[string]any{
		"id": outcome.ID, "status": outcome.Status,
		"session_id": s.ID, "session_name": snap.Name,
		"kind": string(req.Kind), "filename": req.Filename,
	}
	for k, v := range sendResponseFields(snap, mode) {
		resp[k] = v
	}
	if outcome.WAMessageID != "" {
		resp["wa_message_id"] = outcome.WAMessageID
	}
	if outcome.Error != "" {
		resp["error"] = outcome.Error
	}
	writeJSON(w, status, resp)
}

// mediaDownloadRequest is the JSON descriptor POST /media/download accepts
// — field-for-field wa.MediaDescriptor plus the kind DownloadMedia needs
// separately (a descriptor alone does not say which of the four kinds it
// was uploaded as).
type mediaDownloadRequest struct {
	Kind          string `json:"kind"`
	URL           string `json:"url"`
	DirectPath    string `json:"direct_path"`
	MediaKey      string `json:"media_key"`
	FileSHA256    string `json:"file_sha256"`
	FileEncSHA256 string `json:"file_enc_sha256"`
	FileLength    int64  `json:"file_length"`
	MediaType     string `json:"media_type"`
	Mimetype      string `json:"mimetype"`
}

// PostAPISessionMediaDownload is POST /api/v1/sessions/{id}/media/download:
// a previously issued descriptor in, the decrypted bytes out.
//
// Response is JSON with the bytes base64-encoded, NOT a raw
// application/octet-stream body: every other response this API ever sends
// is JSON (writeJSON, api.go), and this is the one place a media byte
// stream could break that uniformly — a raw-binary exception here would be
// a second response convention for exactly one endpoint. A caller that
// wants raw bytes decodes one base64 field.
func (h *handlers) PostAPISessionMediaDownload(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req mediaDownloadRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	kind, ok := parseMediaKind(req.Kind)
	if !ok {
		writeError(w, http.StatusBadRequest, "kind must be one of image, video, audio, document")
		return
	}
	store, err := h.Manager.MediaStore()
	if err != nil {
		writeWAError(w, err)
		return
	}
	desc := wa.MediaDescriptor{
		URL: req.URL, DirectPath: req.DirectPath, MediaKey: req.MediaKey,
		FileSHA256: req.FileSHA256, FileEncSHA256: req.FileEncSHA256,
		FileLength: req.FileLength, MediaType: req.MediaType, Mimetype: req.Mimetype,
	}
	data, err := store.DownloadMedia(r.Context(), s.ID, desc, kind)
	if err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mimetype":    req.Mimetype,
		"size":        len(data),
		"data_base64": base64.StdEncoding.EncodeToString(data),
	})
}
