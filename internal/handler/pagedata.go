package handler

import (
	"net/http"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
)

// PageBase carries the fields every base-layout page needs: the shell chrome
// (title, signed-in username, theme) plus the resolved sidebar nav and the
// NeedECharts flag that gates the vendored ECharts runtime so only chart
// pages load it. Page-specific structs embed this.
type PageBase struct {
	Title           string
	Username        string
	Theme           string
	DarkModeEnabled bool
	Nav             []NavSection
	PlatformLink    NavItem // sidebar footer link back to the backoffice ("" Path = hidden)
	// HostLabel is operator-supplied text naming the machine serving this
	// page ("MINIPC"/"LAPTOP"); "" means render no chip at all — see
	// header.html's {{if .HostLabel}} guard.
	HostLabel   string
	NeedECharts bool
}

// newPageBase assembles the shared page chrome for a request: session
// username, effective theme, and the nav tree with the active branch
// resolved from currentPath (the route the page is served at).
func (h *handlers) newPageBase(r *http.Request, title, currentPath string) PageBase {
	sess, _ := auth.SessionFrom(r.Context())
	return PageBase{
		Title:           title,
		Username:        sess.Username,
		Theme:           h.themeFrom(r),
		DarkModeEnabled: h.DarkMode,
		Nav:             buildNav(h.BasePath, currentPath),
		PlatformLink:    platformLink(h.PlatformURL),
		HostLabel:       h.HostLabel,
	}
}
