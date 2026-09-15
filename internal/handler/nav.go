package handler

import "html/template"

// NavItem is one sidebar entry. Active is computed per request by buildNav so
// the templates stay logic-free.
type NavItem struct {
	Title string
	Path  string
	// source-guards:allow-html — set ONLY from the iconX consts below, which are Go
	// constants and so cannot hold runtime data. Never populated from a request.
	Icon   template.HTML // source-guards:allow-html — see above; inline SVG
	Active bool
}

// NavSection is a labelled group of top-level items (minimalcc "subheader").
type NavSection struct {
	Subheader string
	Items     []NavItem
}

// nav icons (24x24 inline SVG, currentColor) so nothing is loaded from disk or
// a CDN.
const (
	iconDashboard template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M4 13h6a1 1 0 0 0 1-1V4a1 1 0 0 0-1-1H4a1 1 0 0 0-1 1v8a1 1 0 0 0 1 1zm0 8h6a1 1 0 0 0 1-1v-4a1 1 0 0 0-1-1H4a1 1 0 0 0-1 1v4a1 1 0 0 0 1 1zm10 0h6a1 1 0 0 0 1-1v-8a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1v8a1 1 0 0 0 1 1zM13 4v4a1 1 0 0 0 1 1h6a1 1 0 0 0 1-1V4a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1z"/></svg>`
	iconSessions  template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2zm0 2v16h10V4H7zm5 13a1 1 0 1 1 0 2 1 1 0 0 1 0-2z"/></svg>`
	iconSend      template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M2 21l21-9L2 3v7l15 2-15 2z"/></svg>`
	iconClients   template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M12 2a5 5 0 0 1 5 5 5 5 0 0 1-2.1 4.07A8 8 0 0 1 20 18.5V21h-2v-2.5a6 6 0 0 0-12 0V21H4v-2.5a8 8 0 0 1 5.1-7.43A5 5 0 0 1 12 2zm0 2a3 3 0 1 0 0 6 3 3 0 0 0 0-6zm7.5 6H22v2h-2.5v2.5h-2V12H15v-2h2.5V7.5h2V10z"/></svg>`
	iconDocs      template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M6 2a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8l-6-6H6zm7 1.5L18.5 9H13V3.5zM8 12h8v2H8v-2zm0 4h8v2H8v-2z"/></svg>`
	iconLogs      template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M4 5a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V5zm4 3h8V6H8v2zm0 4h8v-2H8v2zm0 4h5v-2H8v2z"/></svg>`
	// paper-plane with a small clock badge — "sends, paced over time",
	// distinct from iconSend's plain paper-plane (fire one message right
	// now) and iconLogs' document glyph (the durable audit log): this page
	// is about the CADENCE between sends on one session, not a single send
	// or the log's per-message record.
	iconOutgoingChat template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M2 21l14-6-14-6v5l9 1-9 1v5z"/><path d="M18 12a4 4 0 1 1 0 8 4 4 0 0 1 0-8zm0 1.5a.75.75 0 0 0-.75.75v1.94l1.35 1.35a.75.75 0 1 0 1.06-1.06l-.91-.91V14.25A.75.75 0 0 0 18 13.5z"/></svg>`
	// heartbeat/pulse line — "is this thing alive right now", distinct from
	// iconLogs' document glyph (the durable message log) so the sidebar
	// itself hints these are two different kinds of "log" before an
	// operator even reads the labels.
	iconSystemLog template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M3 12h4l2-7 4 14 2-7h6v2h-4.5l-2 7-4-14-2 7H3z"/></svg>`
	// left-pointing arrow out of a panel — "leave this module, back to the platform"
	iconPlatform template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M11.7 7.7 8.4 11H20v2H8.4l3.3 3.3-1.4 1.4L4.6 12l5.7-5.7 1.4 1.4zM14 3h5a2 2 0 0 1 2 2v3h-2V5h-5V3zm0 16h5v-3h2v3a2 2 0 0 1-2 2h-5v-2z"/></svg>`
	// handset receiver — "voice calls", distinct from iconSend's paper-plane
	// (a text message) so the sidebar itself hints these dispatch two
	// different kinds of thing before an operator reads the label.
	iconCalls template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M6.6 10.8c1.4 2.8 3.8 5.2 6.6 6.6l2.2-2.2c.3-.3.7-.4 1-.2 1.1.4 2.4.6 3.6.6.6 0 1 .4 1 1V20c0 .6-.4 1-1 1C10.6 21 3 13.4 3 4c0-.6.4-1 1-1h3.5c.6 0 1 .4 1 1 0 1.2.2 2.5.6 3.6.1.4 0 .8-.2 1L6.6 10.8z"/></svg>`
	// checkmark inside a badge/shield outline — "confirm this number before
	// sending to it", distinct from iconSend's paper-plane and iconCalls'
	// handset: this dispatches nothing, it only answers a question.
	iconVerify template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M12 2 4 5v6c0 5.1 3.4 9.7 8 11 4.6-1.3 8-5.9 8-11V5l-8-3zm-1.1 14.2-3.6-3.6 1.4-1.4 2.2 2.2 4.9-4.9 1.4 1.4-6.3 6.3z"/></svg>`
	// two overlapping speech bubbles: "a simulated exchange between two
	// parties", distinct from iconSend's single paper-plane (one outbound
	// message) and iconCalls' handset (one voice call).
	iconSelfConvo template.HTML = `<svg viewBox="0 0 16 16" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M6.5 1a4.5 4.5 0 0 0-3.9 6.7L1.5 10l2.6-.7A4.5 4.5 0 1 0 6.5 1zm4.9 4.6c.06.3.1.6.1.9 0 2.5-2.24 4.5-5 4.5-.24 0-.47-.02-.7-.05A4 4 0 0 0 9.5 15c.5 0 .98-.08 1.43-.22L14 15.5l-.75-1.9A4 4 0 0 0 11.4 5.6z"/></svg>`
	// a calendar face with a clock hand — "this runs again on its own, on a
	// cadence", distinct from iconSelfConvo's speech bubbles (one simulated
	// exchange right now): Convo scheduling turns that one-off into a
	// recurring plan.
	iconSchedules template.HTML = `<svg viewBox="0 0 24 24" width="24" height="24" fill="currentColor" aria-hidden="true"><path d="M7 2a1 1 0 0 1 1 1v1h8V3a1 1 0 1 1 2 0v1h1a2 2 0 0 1 2 2v13a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h1V3a1 1 0 0 1 1-1zM5 10v9h14v-9H5zm8 1.5a3.5 3.5 0 1 1 0 7 3.5 3.5 0 0 1 0-7zm.5 1.5h-1v2.21l1.65 1.65.7-.7-1.35-1.35V13z"/></svg>`
)

// platformLink is the sidebar footer's way back out to the platform backoffice.
// url is absolute (WSM is a separate host in production) and "" hides the link.
func platformLink(url string) NavItem {
	if url == "" {
		return NavItem{}
	}
	return NavItem{Title: "Kembali ke Platform", Path: url, Icon: iconPlatform}
}

// buildNav returns the WSM sidebar tree with Active state resolved against
// currentPath, and every Path rewritten to carry basePath.
//
// The Paths below stay UNPREFIXED as written, and currentPath is unprefixed
// too, because the two are compared to resolve Active state. Prefixing only one
// side would leave every nav item permanently inactive — a highlight that
// silently never lights up, which no test that checks hrefs would catch. The
// prefix is applied in a single pass at the end, after the comparison.
//
// PlatformLink is NOT built here and must never go through that pass: it is an
// absolute URL to a different product (https://app.$DOMAIN/platform/), and
// prefixing it would produce https://app.clino.id/wsm/platform/ — a path that
// belongs to nobody.
func buildNav(basePath, currentPath string) []NavSection {
	sections := []NavSection{
		{Subheader: "Overview", Items: []NavItem{
			{Title: "Dashboard", Path: "/dashboard", Icon: iconDashboard},
		}},
		{Subheader: "WhatsApp", Items: []NavItem{
			{Title: "Sessions", Path: "/sessions", Icon: iconSessions},
			{Title: "Send Message", Path: "/send", Icon: iconSend},
			{Title: "Voice Calls", Path: "/calls", Icon: iconCalls},
			{Title: "Verify Number", Path: "/verify", Icon: iconVerify},
			{Title: "Self Convo", Path: "/self-convo", Icon: iconSelfConvo},
			{Title: "Convo Schedules", Path: "/self-convo/schedules", Icon: iconSchedules},
			{Title: "Message Log", Path: "/logs", Icon: iconLogs},
			{Title: "Outgoing Chat", Path: "/outgoing-chat", Icon: iconOutgoingChat},
		}},
		{Subheader: "Admin", Items: []NavItem{
			{Title: "API Clients", Path: "/admin/api-clients", Icon: iconClients},
			{Title: "System Log", Path: "/system-log", Icon: iconSystemLog},
		}},
		{Subheader: "Docs", Items: []NavItem{
			{Title: "API Docs", Path: "/docs", Icon: iconDocs},
		}},
	}
	for i := range sections {
		for j := range sections[i].Items {
			sections[i].Items[j].Active = sections[i].Items[j].Path == currentPath
			sections[i].Items[j].Path = basePath + sections[i].Items[j].Path
		}
	}
	return sections
}
