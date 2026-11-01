package handler

import (
	"log"
	"net/http"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// This file holds the browser /status page — posting a WhatsApp status/
// story through a session. Deliberately its OWN page (not folded into
// /send): recipients are required here and nowhere else on /send, and the
// confirm-before-posting step (status.js) has nothing in common with
// send.html's routing/media panels. See api_status.go's header comment for
// the shared validation/logging this page's handlers reuse.

// statusColorSwatch is one entry of the text-status background picker.
// ARGB mirrors PostStatusText's own field name and the spec's own decimal
// 0xAARRGGBB convention (/tmp/waxum-status-api.md) — status.html writes it
// straight into the hidden "background_argb" input, so there is no
// hex<->decimal conversion anywhere else in this page's own code.
type statusColorSwatch struct {
	Name string
	Hex  string // CSS colour, for the swatch itself
	ARGB uint32
}

// statusColorSwatches is a small, fixed palette — not configurable, not
// read from waxum (waxum does not expose a palette, only an opaque ARGB
// field) — covering waxum's own documented default (4280184399 =
// 0xFF1E6E4F) plus a few common WhatsApp status colours.
var statusColorSwatches = []statusColorSwatch{
	{Name: "Default", Hex: "#1E6E4F", ARGB: 4280184399},
	{Name: "Teal", Hex: "#075E54", ARGB: 4278222676},
	{Name: "Indigo", Hex: "#4F46E5", ARGB: 4284986341},
	{Name: "Rose", Hex: "#E11D48", ARGB: 4291005256},
	{Name: "Amber", Hex: "#D97706", ARGB: 4290294278},
	{Name: "Slate", Hex: "#334155", ARGB: 4279835989},
}

// statusRecipientOption is one row of the recipients checklist — built
// from wa_contacts (CLAUDE.md's decision): the stored jid when the contact
// has been verified on WhatsApp (contact.JID), else wa.ToJID(contact.Phone)
// — a contact never verified still gets a usable JID to post a status to,
// it is simply unconfirmed whether that number is actually registered.
type statusRecipientOption struct {
	Name  string
	Phone string
	JID   string
}

// statusRecipientOptions builds the checklist from h.Contacts — nil (no
// Contacts repository wired on this deployment) renders an empty list, the
// same "degrade, don't 503, on a page that merely LISTS something optional"
// rule GetSend follows for Deps.Objects.
func (h *handlers) statusRecipientOptions(r *http.Request) []statusRecipientOption {
	if h.Contacts == nil {
		return nil
	}
	cs, err := h.Contacts.List(r.Context())
	if err != nil {
		return nil
	}
	out := make([]statusRecipientOption, 0, len(cs))
	for _, c := range cs {
		jid := c.JID
		if jid == "" {
			jid = wa.ToJID(c.Phone).String()
		}
		out = append(out, statusRecipientOption{Name: c.Name, Phone: c.Phone, JID: jid})
	}
	return out
}

// statusPageData drives GET /status. Connected mirrors sendPageData's own
// field (send.go) — the same "pick any connected session" rule.
type statusPageData struct {
	PageBase
	Connected     []wa.Snapshot
	Swatches      []statusColorSwatch
	Recipients    []statusRecipientOption
	MediaMaxBytes int64
	MediaLimits   []mediaKindDisplay
}

// GetStatus renders the compose page.
func (h *handlers) GetStatus(w http.ResponseWriter, r *http.Request) {
	var connected []wa.Snapshot
	for _, s := range h.Manager.Snapshots() {
		if s.Connected {
			connected = append(connected, s)
		}
	}
	data := statusPageData{
		Connected: connected, Swatches: statusColorSwatches,
		Recipients: h.statusRecipientOptions(r),
		// Image/video status posts go through the SAME upload path ordinary
		// media sends do (api_status.go's postStatusMedia), so the same
		// configured per-kind limits apply — mediaKindDisplays (api_media.go)
		// is the one place those numbers live; status.html must not
		// hardcode its own.
		MediaMaxBytes: h.MediaMaxBytes, MediaLimits: h.mediaKindDisplays(),
	}
	data.PageBase = h.newPageBase(r, "Post Status", "/status")
	h.Renderer.RenderPage(w, "status", data)
}

// statusResultData feeds the status_result partial — the SAME partial
// every outcome renders into, success or failure, mirroring send_result's
// own rule (send.go) that htmx only swaps a 2xx response.
type statusResultData struct {
	Error          string
	WAMessageID    string
	SessionName    string
	RecipientCount int
	Kind           string // "" (text), "image" or "video"
}

func (h *handlers) statusResult(w http.ResponseWriter, data statusResultData) {
	h.Renderer.RenderPartial(w, "status_result", data)
}

// PostStatusText is POST /status/text — the browser form's text panel.
func (h *handlers) PostStatusText(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.statusResult(w, statusResultData{Error: "invalid form"})
		return
	}
	s, ok := h.Manager.Get(r.FormValue("session_id"))
	if !ok {
		h.statusResult(w, statusResultData{Error: "select a connected session"})
		return
	}
	req, errMsg := parseStatusTextFormRequest(r)
	if errMsg != "" {
		h.statusResult(w, statusResultData{Error: errMsg, SessionName: s.Name()})
		return
	}
	sp, err := h.Manager.StatusPoster()
	if err != nil {
		h.statusResult(w, statusResultData{Error: err.Error(), SessionName: s.Name()})
		return
	}
	waID, postErr := sp.PostStatusText(r.Context(), s.ID, req.Text, req.BackgroundARGB, req.Recipients)
	if logErr := h.finishStatusPost(r.Context(), s, req.Text, "", waID, postErr); logErr != nil {
		log.Printf("status post: could not log wa_messages row for session %s (best-effort, continuing): %v", s.ID, logErr)
	}
	if postErr != nil {
		h.statusResult(w, statusResultData{Error: postErr.Error(), SessionName: s.Name()})
		return
	}
	h.statusResult(w, statusResultData{WAMessageID: waID, SessionName: s.Name(), RecipientCount: len(req.Recipients)})
}

// postStatusMediaUI is the shared tail for the browser form's image/video
// panels — mirrors postStatusMedia (api_status.go) but renders the
// status_result partial instead of a JSON body.
func (h *handlers) postStatusMediaUI(w http.ResponseWriter, r *http.Request, kind wa.MediaKind) {
	s, ok := h.Manager.Get(r.FormValue("session_id"))
	if !ok {
		h.statusResult(w, statusResultData{Error: "select a connected session"})
		return
	}
	req, derr := h.parseStatusMediaRequest(w, r, kind)
	if derr != nil {
		h.statusResult(w, statusResultData{Error: derr.msg, SessionName: s.Name()})
		return
	}
	sp, err := h.Manager.StatusPoster()
	if err != nil {
		h.statusResult(w, statusResultData{Error: err.Error(), SessionName: s.Name()})
		return
	}

	msgID := wa.NewID()
	media, err := h.uploadAndDescribeMedia(r.Context(), s, msgID, mediaSendRequest{
		Data: req.Data, Kind: kind, Caption: req.Caption, Filename: req.Filename, ContentType: req.ContentType,
	})
	if err != nil {
		h.statusResult(w, statusResultData{Error: err.Error(), SessionName: s.Name()})
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
	if logErr := h.finishStatusPost(r.Context(), s, req.Caption, string(kind), waID, postErr); logErr != nil {
		log.Printf("status post: could not log wa_messages row for session %s (best-effort, continuing): %v", s.ID, logErr)
	}
	if postErr != nil {
		h.statusResult(w, statusResultData{Error: postErr.Error(), SessionName: s.Name(), Kind: string(kind)})
		return
	}
	h.statusResult(w, statusResultData{
		WAMessageID: waID, SessionName: s.Name(), Kind: string(kind), RecipientCount: len(req.Recipients),
	})
}

// PostStatusImage is POST /status/image.
func (h *handlers) PostStatusImage(w http.ResponseWriter, r *http.Request) {
	h.postStatusMediaUI(w, r, wa.MediaImage)
}

// PostStatusVideo is POST /status/video.
func (h *handlers) PostStatusVideo(w http.ResponseWriter, r *http.Request) {
	h.postStatusMediaUI(w, r, wa.MediaVideo)
}
