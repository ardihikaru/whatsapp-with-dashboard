package handler

import "net/http"

// mountAPI registers the JSON API at the ROOT — deliberately NOT under
// BasePath — plus /openapi.yaml, which the Swagger UI fetches.
//
// ── WHY THIS DOES NOT MOVE WITH THE UI ──────────────────────────────────────
//
// Four callers dial these paths, and none of them goes through the ingress:
//
//	wa-alerter                     WSM_URL + /api/v1/messages
//	netdata's wa-notify.sh         WSM_URL + /api/v1/messages
//	backoffice (notification send) WSM_URL + /api/v1/messages
//	the container healthcheck      /api/v1/health
//
// All four resolve WSM by container DNS on the private network. Prefixing
// /api/v1 would break every one of them to gain nothing, because none of them
// can reach a public path anyway.
//
// It is also what keeps the mount collision-free by construction. Traefik
// routes only PathPrefix(`/wsm`) to this service, so /api/v1 here can never be
// confused with core-api's /api on the same origin — they are different
// services behind different routers, and this one has no public route at all.
//
// ⚠ THAT MEANS THE API IS NOT PUBLICLY REACHABLE, AND THAT IS THE POINT.
// Before Agustus 2026 a bare Host(`wsm.$DOMAIN`) router exposed the whole
// service, JSON API included, to the internet — arguably a bug rather than a
// feature. Anything that needs to call it must be on the private network or
// come through an SSH forward. Publishing it is a security decision, not a
// routing tweak: give it its own router, its own authentication, and say so
// here.
//
// /openapi.yaml stays at the root with the API it describes. The Swagger UI at
// BasePath+"/docs" is a browser page and moves with the UI; the spec is a
// machine-readable artefact of the API and stays with it.
func (h *handlers) mountAPI(mux *http.ServeMux) {
	api := h.Middleware.RequireAPI

	mux.HandleFunc("GET /openapi.yaml", h.GetSpec)

	mux.HandleFunc("POST /api/v1/auth/login", h.PostAPILogin)
	mux.Handle("POST /api/v1/auth/logout", api(http.HandlerFunc(h.PostAPILogout)))
	mux.Handle("GET /api/v1/stats", api(http.HandlerFunc(h.GetStats)))
	mux.HandleFunc("GET /api/v1/health", h.GetHealth)
	mux.Handle("GET /api/v1/sessions", api(http.HandlerFunc(h.GetAPISessions)))
	mux.Handle("POST /api/v1/sessions", api(http.HandlerFunc(h.PostAPISessions)))
	mux.Handle("GET /api/v1/sessions/{id}", api(http.HandlerFunc(h.GetAPISession)))
	mux.Handle("DELETE /api/v1/sessions/{id}", api(http.HandlerFunc(h.DeleteAPISession)))
	mux.Handle("POST /api/v1/sessions/{id}/activate", api(http.HandlerFunc(h.PostAPISessionActivate)))
	mux.Handle("POST /api/v1/sessions/{id}/deactivate", api(http.HandlerFunc(h.PostAPISessionDeactivate)))
	mux.Handle("GET /api/v1/sessions/{id}/pairing", api(http.HandlerFunc(h.GetAPISessionPairing)))
	mux.Handle("POST /api/v1/messages", api(http.HandlerFunc(h.PostAPISendMessageAuto)))
	mux.Handle("POST /api/v1/messages/group", api(http.HandlerFunc(h.PostAPISendGroupMessage)))
	mux.Handle("POST /api/v1/sessions/{id}/messages", api(http.HandlerFunc(h.PostAPISendMessage)))
	mux.Handle("GET /api/v1/sessions/{id}/messages", api(http.HandlerFunc(h.GetAPISessionMessages)))
	mux.Handle("GET /api/v1/sessions/{id}/groups", api(http.HandlerFunc(h.GetAPISessionGroups)))
}
