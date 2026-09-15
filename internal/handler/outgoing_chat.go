package handler

import (
	"html/template"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
)

const (
	outgoingChatPageSize = 10
	// outgoingChatSample bounds GapStats' scan per session — mirrors the
	// plan's chosen sample size (internal/domain/message.GapStats' own doc
	// comment: a low-volume session still gets a meaningful sample, a
	// high-volume one never triggers an unbounded scan).
	outgoingChatSample = 200

	// outgoingSummaryPageSize is the per-page size of the in-memory "Ringkasan
	// per Sesi" table. Manager.Snapshots() is bounded by real device/session
	// count (unlike wa_messages, see the detail table below), so a numbered
	// pager over a plain slice is enough — no keyset cursor needed.
	outgoingSummaryPageSize = 10

	// fallbackSendRate/fallbackSendBurst mirror config.DefaultSendRate/
	// DefaultSendBurst (internal/config/config.go). This package
	// deliberately does not import internal/config (see Deps.MediaMaxBytes'
	// doc comment in routes.go), so any handler test built before
	// Deps.SendRate/SendBurst existed (SendRate/SendBurst left at their zero
	// value) still renders a sane, non-infinite floor rather than "every
	// gap is a violation" or a divide-by-zero.
	fallbackSendRate  float64 = 1.0
	fallbackSendBurst int     = 3

	// outgoingCursorRoot marks "page 1" (an empty keyset cursor) inside the
	// `prev` stack carried in the query string for the detail table's
	// Prev/Next paging (see outgoingRowsURL). A real List() cursor is always
	// "<RFC3339 created_at>\x1f<id>" (message/log_query.go's cursorSep), so
	// it can never collide with this plain marker.
	outgoingCursorRoot = "ROOT"
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
	Filter       outgoingChatFilterView
	SendRate     float64
	SendBurst    int
	FloorLabel   string // "1/SendRate" formatted, e.g. "1.00s" — the expected sustained gap
	Summary      []outgoingSessionView
	SummaryTotal int // sessions across ALL pages, for the "open summary" button label
	Pager        pagerView
	Chart        template.JS
	Rows         outgoingRowsData
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

// outgoingRowsData drives the detail ("Pesan (rincian)") table's Prev/Next
// paging over message.SQLRepository.List's keyset cursor. Unlike an
// append-only "load more", each response is a full page swap (see
// GetOutgoingChat/GetOutgoingChatRows): Page is 1-based, PrevURL/NextURL are
// "" when there is nowhere to go. wa_messages is expected to grow without
// bound (CLAUDE.md's Convo Scheduling retention hazard describes the same
// concern on a sibling table), which is why this stays keyset-based rather
// than becoming OFFSET/LIMIT like the summary table's pager.
type outgoingRowsData struct {
	Rows    []outgoingRowView
	Page    int
	PrevURL string
	NextURL string
	// OOB marks an HTMX full-swap response, same convention as
	// logRowsData.OOB: rendered without the wrapping id/hx-swap-oob
	// attributes on first page load, with them on every /rows response.
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
	data.SummaryTotal = len(summary)
	// The chart plots EVERY session (not just the current summary-table
	// page) — it is its own popup, independently paged from nothing.
	data.Chart = outgoingGapOption(summary, floor)

	// newPager's own links always key on "page" (pager.go); the detail
	// table's paging state lives in `cursor`/`prev` instead (see
	// outgoingRowsData), so there is no collision on this page.
	sPage := pageParam(r.URL.Query().Get("page"))
	data.Pager = newPager(h.BasePath+"/outgoing-chat", outgoingFilterValues(f), sPage, outgoingSummaryPageSize, len(summary))
	from, to := (data.Pager.Page-1)*outgoingSummaryPageSize, (data.Pager.Page-1)*outgoingSummaryPageSize+outgoingSummaryPageSize
	if to > len(summary) {
		to = len(summary)
	}
	if from > to {
		from = to
	}
	data.Summary = summary[from:to]

	rows, next, _ := h.MessageStats.List(ctx, f, "", outgoingChatPageSize)
	data.Rows = outgoingRowsData{
		Rows: toOutgoingRowViews(rows, sessions),
		Page: 1,
	}
	if next != "" {
		data.Rows.NextURL = outgoingRowsURL(h.BasePath, f, next, []string{outgoingCursorRoot})
	}
	data.Filter = outgoingChatFilterView{
		Filter:      f,
		SessionOpts: sessions.opts(),
		StatusOpts:  outgoingStatusOpts,
	}
	h.Renderer.RenderPage(w, "outgoing_chat", data)
}

// GetOutgoingChatRows serves the HTMX table body for a filter + cursor
// state (Prev/Next full-page-swap paging over the keyset cursor — see
// outgoingRowsData's doc comment for why this isn't OFFSET/LIMIT).
func (h *handlers) GetOutgoingChatRows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := parseOutgoingChatFilter(r)
	q := r.URL.Query()
	cursor, prevStack := parseOutgoingCursorState(q)

	rows, next, err := h.MessageStats.List(ctx, f, cursor, outgoingChatPageSize)
	if err != nil {
		http.Error(w, "could not read messages", http.StatusInternalServerError)
		return
	}
	out := outgoingRowsData{
		Rows: toOutgoingRowViews(rows, h.sessionNames()),
		Page: len(prevStack) + 1,
		OOB:  true,
	}
	if next != "" {
		out.NextURL = outgoingRowsURL(h.BasePath, f, next, append(append([]string{}, prevStack...), encodeCursorToken(cursor)))
	}
	if len(prevStack) > 0 {
		last := prevStack[len(prevStack)-1]
		out.PrevURL = outgoingRowsURL(h.BasePath, f, decodeCursorToken(last), prevStack[:len(prevStack)-1])
	}
	h.Renderer.RenderPartial(w, "outgoing_chat_rows_page", out)
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

// parseOutgoingCursorState reads the detail table's paging position from the
// query string: `cursor` is where THIS page's List() call starts ("" = page
// 1), `prev` is a comma-joined stack of the cursors used to reach every
// earlier page (root pages encoded as outgoingCursorRoot — see its doc
// comment) — popping it is how "Sebelumnya" goes back without OFFSET.
func parseOutgoingCursorState(q url.Values) (cursor string, prevStack []string) {
	cursor = q.Get("cursor")
	if raw := q.Get("prev"); raw != "" {
		prevStack = strings.Split(raw, ",")
	}
	return cursor, prevStack
}

func encodeCursorToken(c string) string {
	if c == "" {
		return outgoingCursorRoot
	}
	return c
}

func decodeCursorToken(t string) string {
	if t == outgoingCursorRoot {
		return ""
	}
	return t
}

// outgoingRowsURL builds one detail-table paging link (Prev or Next).
func outgoingRowsURL(basePath string, f message.Filter, cursor string, prevStack []string) string {
	v := outgoingFilterValues(f)
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	if len(prevStack) > 0 {
		v.Set("prev", strings.Join(prevStack, ","))
	}
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

// gapAxisUnit picks ms/s/m/h so the chart's x-axis ticks stay a handful of
// digits wide regardless of how large a session's average gap gets — a
// session idle for 15 minutes between sends previously rendered raw
// millisecond ticks like "900,000", which on a ~360px phone chart don't fit
// ECharts' automatic tick spacing and render overlapping (the bug this
// fixes). maxMS should be the largest value that will actually appear on the
// axis (every bar AND the floor markLine), so the bars and the floor line
// share one consistent, readable unit.
func gapAxisUnit(maxMS int64) (unit string, divisorMS float64) {
	switch {
	case maxMS < 1000:
		return "ms", 1
	case maxMS < 60_000:
		return "s", 1000
	case maxMS < 3_600_000:
		return "m", 60_000
	default:
		return "h", 3_600_000
	}
}

// scaleGapMS converts a raw millisecond value into the chosen axis unit,
// rounded to 2 decimal places so e.g. 900_000ms/60_000 renders as "15" (not
// "15.000000000000002" from float division).
func scaleGapMS(ms int64, divisorMS float64) float64 {
	return math.Round(float64(ms)/divisorMS*100) / 100
}

// outgoingGapOption renders a horizontal bar of each session's average gap
// with a reference line at the configured floor, the fastest way to see at a
// glance whether pacing is holding across the fleet. Follows
// breakdownBarOption's (dashboard_charts.go) shape: reversed so the first
// row draws at the bottom of the y-axis.
//
// The option is transported to the browser as plain JSON (charts-init.js
// does `JSON.parse(blob.textContent)`), so an ECharts axisLabel.formatter
// FUNCTION is not on the table here — JSON cannot carry one. Instead every
// value (bars and the floor markLine alike) is pre-scaled server-side into
// one consistent unit chosen from the largest value that will appear on the
// axis (gapAxisUnit), which is what actually controls how many digits a
// tick label needs. nameLocation/nameGap move the axis name below the tick
// labels instead of colliding with the last one, and axisLabel.hideOverlap
// is defense in depth for whatever ECharts' own auto-tick placement still
// crowds on a narrow chart.
func outgoingGapOption(rows []outgoingSessionView, floor time.Duration) template.JS {
	n := len(rows)
	names := make([]string, n)
	rawMS := make([]int64, n)
	var maxMS int64
	for i, r := range rows {
		names[n-1-i] = r.Name
		rawMS[n-1-i] = r.gapAvgMS
		if r.gapAvgMS > maxMS {
			maxMS = r.gapAvgMS
		}
	}
	floorMS := floor.Milliseconds()
	if floorMS > maxMS {
		maxMS = floorMS
	}
	unit, divisor := gapAxisUnit(maxMS)

	vals := make([]float64, n)
	for i, ms := range rawMS {
		vals[i] = scaleGapMS(ms, divisor)
	}
	floorScaled := scaleGapMS(floorMS, divisor)

	return optionJSON(map[string]any{
		"tooltip": map[string]any{"trigger": "axis"},
		// bottom leaves room for the axis name BELOW the tick labels
		// (nameLocation "middle" + nameGap) instead of the default position,
		// which used to collide with the last tick label.
		"grid": map[string]any{"left": 120, "right": 24, "top": 16, "bottom": 56},
		"xAxis": map[string]any{
			"type":         "value",
			"name":         "avg gap (" + unit + ")",
			"nameLocation": "middle",
			"nameGap":      32,
			"axisLabel":    map[string]any{"hideOverlap": true},
		},
		"yAxis": map[string]any{"type": "category", "data": names},
		"series": []any{
			map[string]any{
				"type": "bar", "barWidth": "52%", "data": vals,
				"markLine": map[string]any{
					"symbol": "none",
					"data":   []any{map[string]any{"xAxis": floorScaled, "name": "floor (1/SendRate)"}},
				},
			},
		},
	})
}
