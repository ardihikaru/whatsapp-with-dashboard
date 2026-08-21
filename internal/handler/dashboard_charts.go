package handler

import (
	"context"
	"encoding/json"
	"html/template"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

// optionJSON marshals an ECharts option map and wraps it in template.JS so
// html/template emits it verbatim inside a <script type="application/json"> blob
// (no HTML-escaping of the JSON). Series colours are left to the theme registered
// in charts-init.js so charts follow light/dark automatically.
func optionJSON(option map[string]any) template.JS {
	b, err := json.Marshal(option)
	if err != nil {
		return template.JS("{}")
	}
	return template.JS(b)
}

// volumeOption — real daily sent-vs-failed volume over 30 days.
func (h *handlers) volumeOption(ctx context.Context) template.JS {
	pts, _ := h.MessageStats.Series(ctx, message.Filter{}, 30)
	return logVolumeOption(pts)
}

// perSessionOption — total volume per WhatsApp session over the last 14 days.
func (h *handlers) perSessionOption(ctx context.Context) template.JS {
	from := time.Now().UTC().AddDate(0, 0, -13).Format("2006-01-02")
	rows, _ := h.MessageStats.Breakdown(ctx, message.Filter{FromDay: from}, "session_id", 12)
	names := h.sessionNames()
	return breakdownBarOption(rows, names.label)
}

// consumersOption — total volume per API consumer over the last 7 days. This is the
// real replacement for the old (dummy) uptime chart: uptime had no historical
// source, whereas consumer attribution now does (0002).
func (h *handlers) consumersOption(ctx context.Context) template.JS {
	from := time.Now().UTC().AddDate(0, 0, -6).Format("2006-01-02")
	rows, _ := h.MessageStats.Breakdown(ctx, message.Filter{FromDay: from}, "api_client_id", 12)
	consumers := h.consumerNames(ctx)
	return breakdownBarOption(rows, consumers.label)
}

// latencyOption — real p50/p95 send latency by hour-of-day over the last 24h.
func (h *handlers) latencyOption(ctx context.Context) template.JS {
	pts, _ := h.MessageStats.Latency(ctx, message.Filter{})
	hours := make([]string, len(pts))
	p50 := make([]int, len(pts))
	p95 := make([]int, len(pts))
	for i, p := range pts {
		hours[i], p50[i], p95[i] = p.Hour, p.P50, p.P95
	}
	return optionJSON(map[string]any{
		"tooltip": map[string]any{"trigger": "axis"},
		"legend":  map[string]any{"top": 0},
		"grid":    map[string]any{"left": 40, "right": 16, "top": 32, "bottom": 32},
		"xAxis":   map[string]any{"type": "category", "boundaryGap": false, "data": hours},
		"yAxis":   map[string]any{"type": "value"},
		"series": []any{
			map[string]any{"name": "p50", "type": "line", "smooth": true, "data": p50},
			map[string]any{"name": "p95", "type": "line", "smooth": true, "data": p95},
		},
	})
}

// breakdownBarOption renders a horizontal bar of a Breakdown, busiest on top,
// resolving each key to a label. Empty when there is nothing to show.
func breakdownBarOption(rows []message.BreakdownRow, label func(string) string) template.JS {
	n := len(rows)
	names := make([]string, n)
	vals := make([]int, n)
	for i, b := range rows {
		// reverse: ECharts draws the first category at the bottom of a y-axis.
		names[n-1-i] = label(b.Key)
		vals[n-1-i] = b.Total
	}
	return optionJSON(map[string]any{
		"tooltip": map[string]any{"trigger": "axis"},
		"grid":    map[string]any{"left": 120, "right": 24, "top": 16, "bottom": 32},
		"xAxis":   map[string]any{"type": "value"},
		"yAxis":   map[string]any{"type": "category", "data": names},
		"series":  []any{map[string]any{"type": "bar", "barWidth": "52%", "data": vals}},
	})
}
