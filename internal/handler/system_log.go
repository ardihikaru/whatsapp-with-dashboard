package handler

import (
	"net/http"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// systemLogLimit is how many of the manager's in-memory ring events the
// page shows at once. Matches wa.Manager.RecentEvents' own ring capacity —
// see that method's doc comment — so this page never implies there is more
// history available than the ring actually holds.
const systemLogLimit = 500

// systemLogPageData drives the full /system-log page. Rows loads and
// re-polls itself over HTMX (see system_log/list.html), the same pattern
// GetSessions/GetSessionRows and GetLogs/GetLogRows already use, so this
// page never embeds row data either.
type systemLogPageData struct {
	PageBase
}

type systemLogRowsData struct {
	Rows []systemLogRowView
}

// systemLogRowView is wa.Event with At pre-formatted — pre-dating
// internal/renderer.New's localtime/fmtTime funcs, this list page formats
// timestamps in its own Go view layer the same way logs_view.go's
// toRowViews does, rather than switching to a template func now that one
// exists.
type systemLogRowView struct {
	At      string
	Level   string
	Session string // SessionName, falling back to SessionID, "" for a manager-level event
	Note    string
}

// toSystemLogRowViews resolves each event's display fields. Session falls
// back to SessionID when SessionName is empty (a session deleted since the
// event was recorded still has a real, if now-orphaned, id worth showing)
// and is "—" only for a genuinely manager-level event (SessionID == "" —
// e.g. boot reconciliation, Manager.RecordEvent's own doc comment). loc is
// the UI display timezone (handlers.loc()).
func toSystemLogRowViews(events []wa.Event, loc *time.Location) []systemLogRowView {
	out := make([]systemLogRowView, 0, len(events))
	for _, e := range events {
		session := e.SessionName
		if session == "" {
			session = e.SessionID
		}
		if session == "" {
			session = "—"
		}
		level := e.Level
		if level == "" {
			level = "info"
		}
		out = append(out, systemLogRowView{
			At: e.At.In(loc).Format("2006-01-02 15:04:05"), Level: level, Session: session, Note: e.Note,
		})
	}
	return out
}

// GetSystemLog renders the in-memory diagnostic event page: state
// transitions, reconnect attempts (including the reconnect supervisor's own
// "needs attention" decision — see internal/wa/reconnect.go), status-poll
// failures, pairing outcomes and send failures, across every session plus
// manager-level events (boot reconciliation, backend errors), newest first.
//
// Deliberately NOT durable. wa.Manager.RecentEvents is backed by an
// in-memory ring lost on every process restart — a high-frequency
// diagnostic trail, not an audit log — and system_log/list.html says so in
// as many words, so nobody mistakes this for one. wa_messages (see /logs,
// internal/domain/message) remains the durable audit log for sends; this
// page must not be confused with it.
func (h *handlers) GetSystemLog(w http.ResponseWriter, r *http.Request) {
	data := systemLogPageData{PageBase: h.newPageBase(r, "System Log", "/system-log")}
	h.Renderer.RenderPage(w, "system_log", data)
}

// GetSystemLogRows serves the polled partial (system_log/list.html's
// hx-trigger) — the same "load, then every Ns" shape /sessions/rows and
// /logs/rows already use, so a slow admin session never has to reload the
// whole page to see what just happened.
func (h *handlers) GetSystemLogRows(w http.ResponseWriter, r *http.Request) {
	h.Renderer.RenderPartial(w, "system_log_rows",
		systemLogRowsData{Rows: toSystemLogRowViews(h.Manager.RecentEvents(systemLogLimit), h.loc())})
}
