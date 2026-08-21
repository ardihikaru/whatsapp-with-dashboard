package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/qr"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

type pairPageData struct {
	PageBase
	Session wa.Snapshot
	Resume  bool // pairing already running: auto-load the status partial
}

// pairStatusData feeds the pairing_status partial. Terminal renders (Done)
// carry no hx-trigger, which is what stops the 2s polling.
type pairStatusData struct {
	SessionID   string
	Mode        wa.PairMode
	Gen         int
	SecondsLeft int
	HasCode     bool
	PhoneCode   string
	PhoneErr    string
	Done        bool
	Err         string
}

// pairStatus derives the partial data: a live/terminal pairing box wins;
// without one, connected means success and anything else means "not pairing".
func pairStatus(s *wa.ManagedSession) pairStatusData {
	if ps, ok := s.PairingSnapshot(); ok {
		return pairStatusData{
			SessionID: s.ID, Mode: ps.Mode, Gen: ps.Gen, SecondsLeft: ps.SecondsLeft,
			HasCode: ps.HasCode, PhoneCode: ps.PhoneCode, PhoneErr: ps.PhoneErr,
			Done: ps.Done, Err: ps.Err,
		}
	}
	if s.Snapshot().State == wa.StateConnected {
		return pairStatusData{SessionID: s.ID, Done: true} // success
	}
	return pairStatusData{SessionID: s.ID, Mode: wa.PairModeQR, Done: true,
		Err: "pairing is not active — restart it"}
}

// GetPair renders the pairing page; already-connected sessions bounce back.
func (h *handlers) GetPair(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	snap := s.Snapshot()
	if snap.State == wa.StateConnected {
		http.Redirect(w, r, h.BasePath+"/sessions", http.StatusSeeOther)
		return
	}
	data := pairPageData{Session: snap, Resume: snap.State == wa.StatePairing}
	data.PageBase = h.newPageBase(r, "Pair Device", "/sessions")
	h.Renderer.RenderPage(w, "pair", data)
}

// PostPairStart kicks off the QR pump (mode=qr|phone) and returns the first
// status partial. A concurrent/duplicate start loses with 409, which HTMX
// ignores — the winner's partial keeps polling.
func (h *handlers) PostPairStart(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	mode := wa.PairMode(r.URL.Query().Get("mode"))
	if mode != wa.PairModePhone {
		mode = wa.PairModeQR
	}
	if err := s.StartPairing(mode); err != nil {
		if errors.Is(err, wa.ErrWrongState) {
			http.Error(w, "session is already pairing or connected", http.StatusConflict)
			return
		}
		slog.Error("start pairing", "session", s.ID, "err", err)
		http.Error(w, "could not start pairing", http.StatusInternalServerError)
		return
	}
	h.Renderer.RenderPartial(w, "pairing_status", pairStatus(s))
}

// GetPairStatus renders the 2s-polled status partial.
func (h *handlers) GetPairStatus(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.Renderer.RenderPartial(w, "pairing_status", pairStatus(s))
}

// GetQRPNG serves the current QR payload as a PNG. The ?g= generation param
// busts the browser cache exactly once per whatsmeow code rotation; 410 tells
// the poller there is nothing to show right now.
func (h *handlers) GetQRPNG(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Manager.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	code, valid := s.PairingCode()
	if !valid {
		http.Error(w, "no active QR code", http.StatusGone)
		return
	}
	png, err := qr.PNG(code, 512)
	if err != nil {
		http.Error(w, "qr encode", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store") // ?g= busts anyway; belt and suspenders
	_, _ = w.Write(png)
}
