package handler

import (
	"context"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

// View models for the /logs templates. Labels (consumer name, session name) are
// resolved here so the templates stay logic-free.

type logFilterView struct {
	message.Filter
	SessionOpts  []kv
	ConsumerOpts []kv
	CategoryOpts []string
}

type breakdownGroup struct {
	ID    string // client | tenant | consumer | session | status | category
	Title string
	Rows  []breakdownRowView
	// Capped is true when the dimension filled the row limit, so the template can
	// say the list is a top-N rather than silently presenting it as complete.
	Capped bool
}

type breakdownRowView struct {
	Label               string
	Total, Sent, Failed int
}

type logRowsData struct {
	Rows    []logRowView
	MoreURL string // "/logs/rows?...&cursor=..." ("" = last page)
	// OOB marks an HTMX append response, where the footer button replaces itself
	// out-of-band instead of being rendered in place.
	OOB bool
}

type logRowView struct {
	CreatedAt   string
	Status      string
	Category    string
	ToPhone     string
	SessionName string
	Consumer    string
	ClientRef   string
	TenantRef   string
	Preview     string
	Error       string
}

type kv struct{ Key, Label string }

// nameMap resolves an attribution key to a human label, with a fallback for the
// empty (un-attributed) key.
type nameMap struct {
	byID     map[string]string
	fallback string
}

func (n nameMap) label(id string) string {
	if id == "" {
		return n.fallback
	}
	if v, ok := n.byID[id]; ok && v != "" {
		return v
	}
	return id
}

func (n nameMap) opts() []kv {
	out := make([]kv, 0, len(n.byID))
	for id, name := range n.byID {
		out = append(out, kv{Key: id, Label: name})
	}
	return out
}

// consumerNames maps api_clients.id → display name (the WSM API consumers).
func (h *handlers) consumerNames(ctx context.Context) nameMap {
	m := nameMap{byID: map[string]string{}, fallback: "Konsol / UI"}
	clients, err := h.APIClients.List(ctx)
	if err != nil {
		return m
	}
	for _, c := range clients {
		m.byID[c.ID] = c.Name
	}
	return m
}

// sessionNames maps wa_sessions.id → display name from live snapshots.
func (h *handlers) sessionNames() nameMap {
	m := nameMap{byID: map[string]string{}, fallback: "(tak diketahui)"}
	for _, s := range h.Manager.Snapshots() {
		m.byID[s.ID] = s.Name
	}
	return m
}

// buildGroups runs one Breakdown per dimension and resolves each key to a label.
func (h *handlers) buildGroups(ctx context.Context, f message.Filter, consumers, sessions nameMap) []breakdownGroup {
	ref := nameMap{fallback: "(tanpa atribusi)"}
	specs := []struct {
		id, title, dimension string
		names                nameMap
	}{
		{"client", "Per Klien", "client_ref", ref},
		{"tenant", "Per Tenant / Outlet", "tenant_ref", ref},
		{"consumer", "Per Konsumen API", "api_client_id", consumers},
		{"session", "Per Sesi WhatsApp", "session_id", sessions},
		{"category", "Per Kategori", "category", nameMap{fallback: "(tanpa kategori)"}},
		{"status", "Per Status", "status", nameMap{fallback: "-"}},
	}
	groups := make([]breakdownGroup, 0, len(specs))
	for _, s := range specs {
		rows, err := h.MessageStats.Breakdown(ctx, f, s.dimension, breakdownLimit)
		if err != nil {
			continue
		}
		g := breakdownGroup{ID: s.id, Title: s.title, Capped: len(rows) >= breakdownLimit}
		for _, b := range rows {
			g.Rows = append(g.Rows, breakdownRowView{
				Label: s.names.label(b.Key), Total: b.Total, Sent: b.Sent, Failed: b.Failed,
			})
		}
		groups = append(groups, g)
	}
	return groups
}

// toRowViews maps repo rows to the table view, resolving names and clipping
// body. loc is the UI display timezone (handlers.loc()) — CreatedAt is
// stored UTC and converted here, the one place html/template has no
// formatter for it (see this package's loc doc comment).
func toRowViews(rows []message.LogRow, consumers, sessions nameMap, loc *time.Location) []logRowView {
	out := make([]logRowView, 0, len(rows))
	for _, m := range rows {
		name := m.SessionName
		if name == "" {
			name = sessions.label(m.SessionID)
		}
		out = append(out, logRowView{
			CreatedAt:   m.CreatedAt.In(loc).Format("2006-01-02 15:04"),
			Status:      m.Status,
			Category:    m.Category,
			ToPhone:     m.ToPhone,
			SessionName: name,
			Consumer:    consumers.label(m.APIClientID),
			ClientRef:   m.ClientRef,
			TenantRef:   m.TenantRef,
			Preview:     clip(m.Body, 80),
			Error:       m.Error,
		})
	}
	return out
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
