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
func (h *handlers) PostAPILogout(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.APIPrincipalFrom(r.Context())
	if principal.TokenHash != "" {
		_ = h.APIClients.RevokeToken(r.Context(), principal.TokenHash)
	}
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
