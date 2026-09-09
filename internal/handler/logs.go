package handler

import (
	"html/template"
	"net/http"
	"net/url"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

const (
	logPageSize = 50
	// breakdownLimit caps each drill-down table; the template says so when it bites.
	breakdownLimit = 20
)

// logsPageData drives the full /logs page: the general summary, a real volume
// chart, the per-dimension drill-downs, and the first page of the message table.
type logsPageData struct {
	PageBase
	Filter      logFilterView
	FilterError string
	Totals      message.Totals
	Volume      template.JS
	Groups      []breakdownGroup
	Rows        logRowsData
}

// GetLogs renders the outbound message log.
func (h *handlers) GetLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := parseLogFilter(r)
	consumers := h.consumerNames(ctx)
	sessions := h.sessionNames()

	data := logsPageData{}
	data.PageBase = h.newPageBase(r, "Log Pesanan Keluar", "/logs")
	data.NeedECharts = true
	data.FilterError = dayRangeError(f)
	data.Totals, _ = h.MessageStats.Totals(ctx, f)
	series, _ := h.MessageStats.Series(ctx, f, 30)
	data.Volume = logVolumeOption(series)
	data.Groups = h.buildGroups(ctx, f, consumers, sessions)

	rows, next, _ := h.MessageStats.List(ctx, f, "", logPageSize)
	data.Rows = logRowsData{Rows: toRowViews(rows, consumers, sessions), MoreURL: moreURL(h.BasePath, f, next)}
	data.Filter = logFilterView{
		Filter:       f,
		SessionOpts:  sessions.opts(),
		ConsumerOpts: consumers.opts(),
		CategoryOpts: logCategories,
	}
	h.Renderer.RenderPage(w, "logs", data)
}

// dayRangeError reports an inverted date range. The filter is still applied
// verbatim (the result is simply empty) — the message explains why.
func dayRangeError(f message.Filter) string {
	if f.FromDay != "" && f.ToDay != "" && f.FromDay > f.ToDay {
		return "Rentang tanggal terbalik: \"Dari\" (" + f.FromDay + ") lebih akhir dari \"Sampai\" (" + f.ToDay + ")."
	}
	return ""
}

// GetLogRows serves the HTMX table body for a filter + cursor (keyset "load more").
func (h *handlers) GetLogRows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := parseLogFilter(r)
	rows, next, err := h.MessageStats.List(ctx, f, r.URL.Query().Get("cursor"), logPageSize)
	if err != nil {
		http.Error(w, "could not read messages", http.StatusInternalServerError)
		return
	}
	h.Renderer.RenderPartial(w, "log_rows_page", logRowsData{
		Rows:    toRowViews(rows, h.consumerNames(ctx), h.sessionNames()),
		MoreURL: moreURL(h.BasePath, f, next),
		OOB:     true,
	})
}

// logCategories is the closed set offered in the filter dropdown. CategorySelfConvo
// is included here even though NormalizeCategory never accepts it from a caller
// (see model.go's doc comment) — a self-convo send is stamped with the category
// directly by internal/selfconvo's gateway adapter, and an operator still needs to
// be able to filter the log down to it.
var logCategories = []string{
	message.CategoryOTP, message.CategoryNotification, message.CategoryAlert, message.CategoryManual,
	message.CategorySelfConvo,
}

// parseLogFilter reads the filter from the query string. Unknown values are simply
// carried as-is; the repo treats an empty field as "no constraint".
func parseLogFilter(r *http.Request) message.Filter {
	q := r.URL.Query()
	return message.Filter{
		Status:      q.Get("status"),
		SessionID:   q.Get("session_id"),
		APIClientID: q.Get("api_client_id"),
		ClientRef:   q.Get("client_ref"),
		TenantRef:   q.Get("tenant_ref"),
		Category:    q.Get("category"),
		FromDay:     q.Get("from"),
		ToDay:       q.Get("to"),
	}
}

// logFilterValues serialises the active filter to url.Values (no cursor).
func logFilterValues(f message.Filter) url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("status", f.Status)
	set("session_id", f.SessionID)
	set("api_client_id", f.APIClientID)
	set("client_ref", f.ClientRef)
	set("tenant_ref", f.TenantRef)
	set("category", f.Category)
	set("from", f.FromDay)
	set("to", f.ToDay)
	return v
}

// moreURL builds the keyset "load more" URL, URL-encoding the opaque cursor (it
// carries a \x1f separator and a colon-bearing timestamp). "" when there is no
// next page.
func moreURL(basePath string, f message.Filter, cursor string) string {
	if cursor == "" {
		return ""
	}
	v := logFilterValues(f)
	v.Set("cursor", cursor)
	return basePath + "/logs/rows?" + v.Encode()
}

// logVolumeOption builds the real 30-day sent-vs-failed volume chart (ECharts).
func logVolumeOption(pts []message.DailyPoint) template.JS {
	days := make([]string, len(pts))
	sent := make([]int, len(pts))
	failed := make([]int, len(pts))
	for i, p := range pts {
		days[i], sent[i], failed[i] = p.Day, p.Sent, p.Failed
	}
	return optionJSON(map[string]any{
		"tooltip": map[string]any{"trigger": "axis"},
		"legend":  map[string]any{"top": 0},
		"grid":    map[string]any{"left": 40, "right": 16, "top": 32, "bottom": 32},
		"xAxis":   map[string]any{"type": "category", "boundaryGap": false, "data": days},
		"yAxis":   map[string]any{"type": "value"},
		"series": []any{
			map[string]any{"name": "Terkirim", "type": "line", "smooth": true, "areaStyle": map[string]any{"opacity": 0.12}, "data": sent},
			map[string]any{"name": "Gagal", "type": "line", "smooth": true, "data": failed},
		},
	})
}
