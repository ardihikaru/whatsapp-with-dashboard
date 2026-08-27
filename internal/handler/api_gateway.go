package handler

import "net/http"

// GetAPIGatewayHealth is GET /api/v1/gateway/health — Phase 6b §3.
// AUTHENTICATED (RequireAPI), unlike GET /api/v1/health, and DELIBERATELY
// NOT the same route: that one is unauthenticated and is the container
// healthcheck (docker-compose.yml), probed on every liveness interval, and
// reads a CACHED verdict instead (api.go's GetHealth, since Phase 8's SSE
// event pump). THIS route is operator-initiated (a manual/dashboard check,
// not a liveness probe on a fixed interval), so it stays a live call
// straight through to Manager.GatewayHealth, which answers
// wa.ErrUnsupported (501) on the whatsmeow backend, since whatsmeow has no
// separate gateway process to probe.
func (h *handlers) GetAPIGatewayHealth(w http.ResponseWriter, r *http.Request) {
	if err := h.Manager.GatewayHealth(r.Context()); err != nil {
		writeWAError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
