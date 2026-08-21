package handler

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

// widget is one minimalcc-style summary card. Value is a preformatted string so a
// KPI can be a percentage or a "348 ms" duration, not just a number. Every tile is
// now real, read from the wa_messages audit log.
type widget struct {
	Label   string
	Value   string
	Variant string
}

type dashboardData struct {
	PageBase
	Widgets     []widget
	Volume      template.JS
	PerSession  template.JS
	Latency     template.JS
	Consumers   template.JS
	GeneratedAt time.Time
}

// activeSessionsCount is the live count of connected sessions from the manager.
func (h *handlers) activeSessionsCount() int {
	_, connected := h.Manager.Counts()
	return connected
}

func (h *handlers) buildWidgets(ctx context.Context) []widget {
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	weekAgo := now.AddDate(0, 0, -6).Format("2006-01-02")
	todayTot, _ := h.MessageStats.Totals(ctx, message.Filter{FromDay: today, ToDay: today})
	weekTot, _ := h.MessageStats.Totals(ctx, message.Filter{FromDay: weekAgo})
	return []widget{
		{"Pesan hari ini", strconv.Itoa(todayTot.Total), "primary"},
		{"Tingkat sukses (7h)", successRate(weekTot), "success"},
		{"Sesi aktif", strconv.Itoa(h.activeSessionsCount()), "info"},
		{"Latensi p50 (24j)", h.avgLatency(ctx), "warning"},
	}
}

// successRate is sent / (sent + failed); "—" when nothing terminal has been sent
// so the tile never claims a hollow 100%.
func successRate(t message.Totals) string {
	den := t.Sent + t.Failed
	if den == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(t.Sent)/float64(den))
}

// avgLatency is the mean of the non-empty hourly p50 buckets over the last 24h.
func (h *handlers) avgLatency(ctx context.Context) string {
	pts, err := h.MessageStats.Latency(ctx, message.Filter{})
	if err != nil {
		return "—"
	}
	sum, n := 0, 0
	for _, p := range pts {
		if p.P50 > 0 {
			sum += p.P50
			n++
		}
	}
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("%d ms", sum/n)
}

func (h *handlers) widgetData(ctx context.Context) dashboardData {
	return dashboardData{
		Widgets:     h.buildWidgets(ctx),
		GeneratedAt: time.Now(),
	}
}

// GetDashboard renders the full dashboard page: KPI tiles + 4 real ECharts.
func (h *handlers) GetDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data := h.widgetData(ctx)
	data.PageBase = h.newPageBase(r, "Dashboard", "/dashboard")
	data.NeedECharts = true
	data.Volume = h.volumeOption(ctx)
	data.PerSession = h.perSessionOption(ctx)
	data.Latency = h.latencyOption(ctx)
	data.Consumers = h.consumersOption(ctx)
	h.Renderer.RenderPage(w, "dashboard", data)
}

// GetWidgets renders just the widget grid for HTMX polling/refresh.
func (h *handlers) GetWidgets(w http.ResponseWriter, r *http.Request) {
	h.Renderer.RenderPartial(w, "widgets", h.widgetData(r.Context()))
}
