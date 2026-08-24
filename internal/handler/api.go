package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
)

const maxBodyBytes = 64 * 1024

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	LoginID  string `json:"login_id"`
	Secret   string `json:"secret"`
}

type loginResponse struct {
	Token     string  `json:"token"`
	ExpiresAt *string `json:"expires_at"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// backendBusyRetryAfterSeconds is the Retry-After hint sent alongside
// wa.ErrBackendBusy (see writeBackendBusy). It is a fixed, conservative
// value rather than something derived from waxum's own Retry-After header:
// by the time a handler sees ErrBackendBusy, internal/waxum.Client.do has
// already retried the underlying 429 with backoff (honouring any
// Retry-After waxum sent) and given up — this is what a CALLER of WSM's own
// API should wait before trying again, a separate question from what WSM
// itself waited internally.
const backendBusyRetryAfterSeconds = "5"

// writeBackendBusy answers wa.ErrBackendBusy: 503, not 429. 429 already
// means something else in this API (wa.ErrQueueFull — THIS session's own
// send queue is full, see errors.go); reusing it here would tell an API
// consumer it is the one being throttled, when the actual cause is the
// WhatsApp backend (waxum) rate-limiting WSM itself. 503 + Retry-After is
// the more honest signal: temporarily unavailable, retry shortly, not "you
// are sending too fast".
func writeBackendBusy(w http.ResponseWriter, message string) {
	w.Header().Set("Retry-After", backendBusyRetryAfterSeconds)
	writeError(w, http.StatusServiceUnavailable, message)
}

// PostAPILogin authenticates an API client and returns a bearer access token.
func (h *handlers) PostAPILogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	loginID := strings.TrimSpace(req.LoginID)
	secret := req.Secret
	if loginID == "" && strings.TrimSpace(req.Username) != "" {
		loginID = strings.TrimSpace(req.Username)
		secret = req.Password
	}
	issued, err := h.APIClients.Login(r.Context(), loginID, secret)
	if err != nil {
		if errors.Is(err, apiclient.ErrInactiveClient) {
			writeError(w, http.StatusUnauthorized, "api client is inactive")
			return
		}
		if errors.Is(err, apiclient.ErrExpiredClient) {
			writeError(w, http.StatusUnauthorized, "api client access period has ended")
			return
		}
		if errors.Is(err, apiclient.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid login id or secret")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create token")
		return
	}
	var expires *string
	if issued.ExpiresAt != nil {
		v := issued.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
		expires = &v
	}
	writeJSON(w, http.StatusOK, loginResponse{
		Token:     issued.Token,
		ExpiresAt: expires,
	})
}

// PostAPILogout revokes the current API-client bearer token.
// PostAPILogout revokes the bearer token that authenticated this request.
//
// It is meaningful ONLY on the bearer path. A caller authenticated with HTTP
// Basic minted no token, so there is nothing to revoke — answering 204 there
// would report success for work that did not happen, and leave someone
// believing they had cut off access they still have. Revoking Basic access
// means deactivating the client or rotating its secret.
func (h *handlers) PostAPILogout(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.APIPrincipalFrom(r.Context())
	if principal.TokenHash == "" {
		writeError(w, http.StatusBadRequest,
			"logout applies to bearer tokens only; a Basic-authenticated client has no token to revoke")
		return
	}
	_ = h.APIClients.RevokeToken(r.Context(), principal.TokenHash)
	w.WriteHeader(http.StatusNoContent)
}

// GetStats returns the dashboard KPI values as JSON.
func (h *handlers) GetStats(w http.ResponseWriter, r *http.Request) {
	widgets := h.buildWidgets(r.Context())
	out := make(map[string]string, len(widgets))
	for _, wd := range widgets {
		out[wd.Label] = wd.Value
	}
	writeJSON(w, http.StatusOK, out)
}

// GetHealth reports process health with real session counts. No DB ping
// needed — SQLite is local to the process.
func (h *handlers) GetHealth(w http.ResponseWriter, r *http.Request) {
	total, connected := h.Manager.Counts()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"sessions":  total,
		"connected": connected,
	})
}
