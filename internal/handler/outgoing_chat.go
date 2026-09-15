package handler

import (
	"html/template"
	"net/http"
	"net/url"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

const (
	outgoingChatPageSize = 50
	// outgoingChatSample bounds GapStats' scan per session — mirrors the
	// plan's chosen sample size (internal/domain/message.GapStats' own doc
	// comment: a low-volume session still gets a meaningful sample, a
	// high-volume one never triggers an unbounded scan).
	outgoingChatSample = 200

	// fallbackSendRate/fallbackSendBurst mirror config.DefaultSendRate/
	// DefaultSendBurst (internal/config/config.go). This package
	// deliberately does not import internal/config (see Deps.MediaMaxBytes'
	// doc comment in routes.go), so any handler test built before
	// Deps.SendRate/SendBurst existed (SendRate/SendBurst left at their zero
	// value) still renders a sane, non-infinite floor rather than "every
	// gap is a violation" or a divide-by-zero.
	fallbackSendRate  float64 = 1.0
	fallbackSendBurst int     = 3
)

// effectiveSendRate/effectiveSendBurst apply the fallback above when Deps
// was built without the real config value.
func (h *handlers) effectiveSendRate() float64 {
	if h.SendRate <= 0 {
		return fallbackSendRate
	}
	return h.SendRate
}

func (h *handlers) effectiveSendBurst() int {
	if h.SendBurst <= 0 {
		return fallbackSendBurst
	}
	return h.SendBurst
}

// outgoingChatPageData drives the full /outgoing-chat page: a per-session
// pacing summary (real gap statistics, not simulated — see
// message.GapStats' doc comment), a chart of average gap vs. the configured
// floor, and the first page of the raw per-message log, filterable by
// session and status.
type outgoingChatPageData struct {
	PageBase
	Filter     outgoingChatFilterView
	SendRate   float64
	SendBurst  int
	FloorLabel string // "1/SendRate" formatted, e.g. "1.00s" — the expected sustained gap
	Summary    []outgoingSessionView
	Chart      template.JS
	Rows       outgoingRowsData
}

// outgoingSessionView is one row of the summary table: everything an
// operator needs to judge whether a session's real send cadence matches the
// configured rate limit.
type outgoingSessionView struct {
	ID, Name, Phone, JID, State string
	Sent, Failed                int
	GapCount                    int
	GapMin, GapMax, GapAvg      string // formatted, "—" when GapCount == 0
	gapAvgMS                    int64  // raw milliseconds, for the chart only
	// Violation flags a session whose AVERAGE gap over the sampled window is
	// below the configured floor (1/SendRate) — see the plan's "violation
	// heuristic" doc: a burst absorbs only the first SendBurst sends, so an
	// average below the floor over a 200-sample window means the bucket is
	// being outrun consistently, not just bursting. Deliberately NOT a
	// per-pair MIN check: MIN alone is expected to dip below the floor
	// during a legitimate burst.
	Violation bool
}

type outgoingChatFilterView struct {
	message.Filter
	SessionOpts []kv
	StatusOpts  []string
}

type outgoingRowsData struct {
	Rows    []outgoingRowView
	MoreURL string // "/outgoing-chat/rows?...&cursor=..." ("" = last page)
	// OOB marks an HTMX append response, same convention as logRowsData.OOB.
	OOB bool
}

type outgoingRowView struct {
	CreatedAt   string
	Status      string
	ToPhone     string
	SessionName string
	Error       string
	Attempts    int
}

// outgoingStatusOpts is the closed set offered in the filter dropdown.
var outgoingStatusOpts = []string{message.StatusQueued, message.StatusSent, message.StatusFailed}

// GetOutgoingChat renders the outbound send-pacing page: for every session
// (connected or not), the WA number, live send/fail counts, and the
// MIN/MAX/AVERAGE gap between consecutive successful sends, computed from
// the real sent_at timestamps already sitting in wa_messages
// (message.SQLRepository.GapStats) — an INFERENCE from observed outcomes,
// not a direct trace of internal/wa/sender_limits.go's token-bucket
// decisions. A session can legitimately show a MIN gap narrower than the
// configured floor during a burst (SendBurst absorbs that); see Violation's
// doc comment for the heuristic this page actually flags on.
func (h *handlers) GetOutgoingChat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := parseOutgoingChatFilter(r)
	sessions := h.sessionNames()

	rate := h.effectiveSendRate()
	burst := h.effectiveSendBurst()
	floor := time.Duration(float64(time.Second) / rate)

	snaps := h.Manager.Snapshots()
	summary := make([]outgoingSessionView, 0, len(snaps))
	for _, s := range snaps {
		totals, _ := h.MessageStats.Totals(ctx, message.Filter{SessionID: s.ID})
		gap, _ := h.MessageStats.GapStats(ctx, s.ID, outgoingChatSample)
		jid := s.JID
		if jid == "" {
			jid = s.Phone
		}
		v := outgoingSessionView{
			ID: s.ID, Name: s.Name, Phone: s.Phone, JID: jid, State: string(s.State),
			Sent: totals.Sent, Failed: totals.Failed, GapCount: gap.Count,
		}
		if gap.Count > 0 {
			v.GapMin = gap.Min.Round(time.Millisecond * 10).String()
			v.GapMax = gap.Max.Round(time.Millisecond * 10).String()
			v.GapAvg = gap.Avg.Round(time.Millisecond * 10).String()
			v.gapAvgMS = gap.Avg.Milliseconds()
			v.Violation = gap.Avg < floor
		} else {
			v.GapMin, v.GapMax, v.GapAvg = "—", "—", "—"
		}
		summary = append(summary, v)
	}

	data := outgoingChatPageData{}
	data.PageBase = h.newPageBase(r, "Outgoing Chat", "/outgoing-chat")
	data.NeedECharts = true
	data.SendRate = rate
	data.SendBurst = burst
	data.FloorLabel = floor.Round(time.Millisecond * 10).String()
	data.Summary = summary
	data.Chart = outgoingGapOption(summary, floor)

	rows, next, _ := h.MessageStats.List(ctx, f, "", outgoingChatPageSize)
	data.Rows = outgoingRowsData{
		Rows:    toOutgoingRowViews(rows, sessions),
		MoreURL: outgoingMoreURL(h.BasePath, f, next),
	}
	data.Filter = outgoingChatFilterView{
		Filter:      f,
		SessionOpts: sessions.opts(),
		StatusOpts:  outgoingStatusOpts,
	}
	h.Renderer.RenderPage(w, "outgoing_chat", data)
}

// GetOutgoingChatRows serves the HTMX table body for a filter + cursor
// (keyset "load more"), the same shape GetLogRows uses.
func (h *handlers) GetOutgoingChatRows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := parseOutgoingChatFilter(r)
	rows, next, err := h.MessageStats.List(ctx, f, r.URL.Query().Get("cursor"), outgoingChatPageSize)
	if err != nil {
		http.Error(w, "could not read messages", http.StatusInternalServerError)
		return
	}
	h.Renderer.RenderPartial(w, "outgoing_chat_rows_page", outgoingRowsData{
		Rows:    toOutgoingRowViews(rows, h.sessionNames()),
		MoreURL: outgoingMoreURL(h.BasePath, f, next),
		OOB:     true,
	})
}

// parseOutgoingChatFilter reads this page's (deliberately narrower than
// /logs') filter from the query string: session + status only.
func parseOutgoingChatFilter(r *http.Request) message.Filter {
	q := r.URL.Query()
	return message.Filter{
		SessionID: q.Get("session_id"),
		Status:    q.Get("status"),
	}
}

// outgoingFilterValues serialises the active filter to url.Values (no cursor).
func outgoingFilterValues(f message.Filter) url.Values {
	v := url.Values{}
	if f.SessionID != "" {
		v.Set("session_id", f.SessionID)
	}
	if f.Status != "" {
		v.Set("status", f.Status)
	}
	return v
}

// outgoingMoreURL builds the keyset "load more" URL. "" when there is no next page.
func outgoingMoreURL(basePath string, f message.Filter, cursor string) string {
	if cursor == "" {
		return ""
	}
	v := outgoingFilterValues(f)
	v.Set("cursor", cursor)
	return basePath + "/outgoing-chat/rows?" + v.Encode()
}

// toOutgoingRowViews maps repo rows to the detail table view: to which
// number, success or failure, at what time — resolving the session name the
// same way toRowViews (logs_view.go) does.
func toOutgoingRowViews(rows []message.LogRow, sessions nameMap) []outgoingRowView {
	out := make([]outgoingRowView, 0, len(rows))
	for _, m := range rows {
		name := m.SessionName
		if name == "" {
			name = sessions.label(m.SessionID)
		}
		out = append(out, outgoingRowView{
			CreatedAt:   m.CreatedAt.Format("2006-01-02 15:04:05"),
			Status:      m.Status,
			ToPhone:     m.ToPhone,
			SessionName: name,
			Error:       m.Error,
			Attempts:    m.Attempts,
		})
	}
	return out
}

// outgoingGapOption renders a horizontal bar of each session's average gap
// (ms) with a reference line at the configured floor, the fastest way to
// see at a glance whether pacing is holding across the fleet. Follows
// breakdownBarOption's (dashboard_charts.go) shape: reversed so the first
// row draws at the bottom of the y-axis.
func outgoingGapOption(rows []outgoingSessionView, floor time.Duration) template.JS {
	n := len(rows)
	names := make([]string, n)
	vals := make([]int64, n)
	for i, r := range rows {
		names[n-1-i] = r.Name
		vals[n-1-i] = r.gapAvgMS
	}
	return optionJSON(map[string]any{
		"tooltip": map[string]any{"trigger": "axis"},
		"grid":    map[string]any{"left": 120, "right": 24, "top": 16, "bottom": 32},
		"xAxis":   map[string]any{"type": "value", "name": "avg gap (ms)"},
		"yAxis":   map[string]any{"type": "category", "data": names},
		"series": []any{
			map[string]any{
				"type": "bar", "barWidth": "52%", "data": vals,
				"markLine": map[string]any{
					"symbol": "none",
					"data":   []any{map[string]any{"xAxis": floor.Milliseconds(), "name": "floor (1/SendRate)"}},
				},
			},
		},
	})
}
