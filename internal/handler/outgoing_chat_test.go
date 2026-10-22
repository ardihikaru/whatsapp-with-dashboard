package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// newOutgoingChatTestServer mirrors newLogsTestServer (logs_test.go) but
// additionally registers sessions in the LIVE manager (via manager.Create)
// rather than only inserting a wa_sessions row directly — GetOutgoingChat
// enumerates h.Manager.Snapshots() (every session, connected or not), which
// a nil-backend manager never populates on its own. sendRate<=0 leaves
// Deps.SendRate at its zero value, exercising the fallback in
// effectiveSendRate.
func newOutgoingChatTestServer(t *testing.T, sendRate float64, sendBurst int) (http.Handler, *auth.Store, *sql.DB, *message.SQLRepository, *wa.Manager) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	ctx := context.Background()
	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	msgRepo := message.NewRepository(pool)
	manager := wa.NewManager(ctx, nil, wasession.NewRepository(pool), msgRepo, nil,
		wa.Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
	sessions := auth.NewStore(time.Hour)
	t.Cleanup(sessions.Close)
	apiClients := apiclient.NewRepository(pool)
	srv := New(Deps{
		Renderer: rend, Sessions: sessions,
		Middleware: auth.NewMiddleware(sessions, apiClients, ""), Manager: manager,
		Messages: msgRepo, MessageStats: msgRepo, APIClients: apiClients,
		SessionTTL: time.Hour, AssetsFS: wsm.FS,
		PlatformURL: "https://clino.test/platform/",
		SendRate:    sendRate, SendBurst: sendBurst,
	})
	return srv, sessions, pool, msgRepo, manager
}

// seedSentGap inserts n successful sends on sessionID, `gap` apart, starting
// at base, writing sent_at directly via raw SQL (not repo.Insert/MarkSent,
// which can only stamp time.Now()) — this test's whole point is to pin an
// exact, known cadence, the same reason message/gap_test.go seeds the same
// way.
func seedSentGap(t *testing.T, pool *sql.DB, sessionID string, n int, base time.Time, gap time.Duration, toPhone string) {
	t.Helper()
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * gap).Format(time.RFC3339)
		id := fmt.Sprintf("%s-m%d", sessionID, i)
		if _, err := pool.ExecContext(context.Background(),
			`INSERT INTO wa_messages (id, session_id, to_phone, body, status, created_at, sent_at)
			 VALUES (?, ?, ?, 'hi', 'sent', ?, ?)`, id, sessionID, toPhone, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOutgoingChatPageRendersRealData(t *testing.T) {
	srv, sessions, pool, repo, manager := newOutgoingChatTestServer(t, 1.0, 3)
	ctx := context.Background()

	paced, err := manager.Create(ctx, "Gateway Paced", "628111000")
	if err != nil {
		t.Fatal(err)
	}
	fast, err := manager.Create(ctx, "Gateway TooFast", "628222000")
	if err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Add(-time.Hour)
	// Paced: 2s apart, well above the 1s floor (SendRate=1.0).
	seedSentGap(t, pool, paced.ID, 3, base, 2*time.Second, "628999111")
	// TooFast: 100ms apart, well below the 1s floor.
	seedSentGap(t, pool, fast.ID, 3, base, 100*time.Millisecond, "628999222")
	// A failed send on the paced session, to prove Failed count is real.
	if err := repo.Insert(ctx, &message.Message{ID: "mfail", SessionID: paced.ID, ToPhone: "628999333", Body: "x", Status: message.StatusFailed}); err != nil {
		t.Fatal(err)
	}

	res := authedGet(t, srv, sessions, "/outgoing-chat")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /outgoing-chat = %d: %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{
		"Outgoing Chat", "Gateway Paced", "628111000", "Gateway TooFast", "628222000",
		"2s", // Paced's Min/Max/Avg gap
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/outgoing-chat body missing %q; body:\n%s", want, body)
		}
	}

	rows := authedGet(t, srv, sessions, "/outgoing-chat/rows")
	if rows.Code != http.StatusOK {
		t.Fatalf("GET /outgoing-chat/rows = %d", rows.Code)
	}
	if !strings.Contains(rows.Body.String(), "628999222") {
		t.Fatalf("/outgoing-chat/rows missing a TooFast-session message row")
	}
}

// TestOutgoingChatPageRequiresSession: platform-only, same guard as /logs
// (TestLogsPageRequiresSession).
func TestOutgoingChatPageRequiresSession(t *testing.T) {
	srv, _, _, _, _ := newOutgoingChatTestServer(t, 1.0, 3)
	req := httptest.NewRequest(http.MethodGet, "/outgoing-chat", nil)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("unauth GET /outgoing-chat = %d, want 303 redirect to login", res.Code)
	}
}

// TestOutgoingChatFlagsASessionAveragingBelowTheConfiguredFloor is the test
// that would fail if the violation heuristic's direction were inverted (see
// outgoing_chat.go's Violation doc comment): a session whose average gap
// over the sampled window sits BELOW 1/SendRate must render the warning.
func TestOutgoingChatFlagsASessionAveragingBelowTheConfiguredFloor(t *testing.T) {
	srv, sessions, pool, _, manager := newOutgoingChatTestServer(t, 1.0, 3) // floor = 1s
	ctx := context.Background()
	fast, err := manager.Create(ctx, "Gateway TooFast", "628222000")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	seedSentGap(t, pool, fast.ID, 5, base, 100*time.Millisecond, "628999222")

	body := authedGet(t, srv, sessions, "/outgoing-chat").Body.String()
	if !strings.Contains(body, "di bawah batas") {
		t.Fatalf("a session averaging 100ms against a 1s floor is not flagged; body:\n%s", body)
	}
}

// TestOutgoingChatDoesNotFlagASessionPacedAboveTheFloor is the inverse case:
// a session comfortably above the floor must render no warning at all.
func TestOutgoingChatDoesNotFlagASessionPacedAboveTheFloor(t *testing.T) {
	srv, sessions, pool, _, manager := newOutgoingChatTestServer(t, 1.0, 3) // floor = 1s
	ctx := context.Background()
	paced, err := manager.Create(ctx, "Gateway Paced", "628111000")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	seedSentGap(t, pool, paced.ID, 5, base, 2*time.Second, "628999111")

	body := authedGet(t, srv, sessions, "/outgoing-chat").Body.String()
	if strings.Contains(body, "di bawah batas") {
		t.Fatalf("a session averaging 2s against a 1s floor must not be flagged; body:\n%s", body)
	}
}

// seedManyOutgoingMessages inserts n successful sends on sessionID, spaced 1s
// apart with a distinct ToPhone per row (628000000+i), so a test can tell
// which page of message.SQLRepository.List's keyset pagination it is looking
// at by which ToPhone values are present.
func seedManyOutgoingMessages(t *testing.T, pool *sql.DB, sessionID string, n int, base time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		id := fmt.Sprintf("%s-row%03d", sessionID, i)
		phone := fmt.Sprintf("628%06d", i)
		if _, err := pool.ExecContext(context.Background(),
			`INSERT INTO wa_messages (id, session_id, to_phone, body, status, created_at, sent_at)
			 VALUES (?, ?, ?, 'hi', 'sent', ?, ?)`, id, sessionID, phone, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
}

// TestOutgoingChatDetailTablePagesForwardAndBackOverTheKeysetCursor is the
// red-without-the-change test for item 3's detail table: before this change
// the only control was an append-only "Muat 50 lagi" button (MoreURL) and
// there was no way back. Seeds 12 messages (> outgoingChatPageSize=10) so
// there IS a second page, walks Next then Back, and checks the page counter
// and which rows are visible at each step.
func TestOutgoingChatDetailTablePagesForwardAndBackOverTheKeysetCursor(t *testing.T) {
	srv, sessions, pool, _, manager := newOutgoingChatTestServer(t, 1.0, 3)
	ctx := context.Background()
	sess, err := manager.Create(ctx, "Gateway Many", "628333000")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	seedManyOutgoingMessages(t, pool, sess.ID, 12, base)
	newestPhone := "628000011" // i=11, the most recently created row
	oldestPhone := "628000000" // i=0, only reachable on page 2

	page1 := authedGet(t, srv, sessions, "/outgoing-chat")
	if page1.Code != http.StatusOK {
		t.Fatalf("GET /outgoing-chat = %d: %s", page1.Code, page1.Body.String())
	}
	body1 := page1.Body.String()
	if !strings.Contains(body1, "Halaman 1") {
		t.Fatalf("page 1 does not render its page counter; body:\n%s", body1)
	}
	if !strings.Contains(body1, newestPhone) {
		t.Fatalf("page 1 is missing the newest row %q; body:\n%s", newestPhone, body1)
	}
	if strings.Contains(body1, oldestPhone) {
		t.Fatalf("page 1 already shows the oldest row %q — pagination is not limiting the page", oldestPhone)
	}
	if strings.Contains(body1, "Sebelumnya") {
		t.Fatalf("page 1 must not render a Prev control, there is nowhere to go back to; body:\n%s", body1)
	}

	nextURL := extractHxGetURL(t, body1, "Berikutnya")
	page2 := authedGet(t, srv, sessions, nextURL)
	if page2.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", nextURL, page2.Code, page2.Body.String())
	}
	body2 := page2.Body.String()
	if !strings.Contains(body2, "Halaman 2") {
		t.Fatalf("page 2 does not render its page counter; body:\n%s", body2)
	}
	if !strings.Contains(body2, oldestPhone) {
		t.Fatalf("page 2 is missing the oldest row %q; body:\n%s", oldestPhone, body2)
	}
	if strings.Contains(body2, newestPhone) {
		t.Fatalf("page 2 still shows the newest row %q; body:\n%s", newestPhone, body2)
	}
	if !strings.Contains(body2, "Sebelumnya") {
		t.Fatalf("page 2 must render a Prev control; body:\n%s", body2)
	}

	prevURL := extractHxGetURL(t, body2, "Sebelumnya")
	back := authedGet(t, srv, sessions, prevURL)
	if back.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", prevURL, back.Code, back.Body.String())
	}
	bodyBack := back.Body.String()
	if !strings.Contains(bodyBack, "Halaman 1") {
		t.Fatalf("stepping back does not return to page 1; body:\n%s", bodyBack)
	}
	if !strings.Contains(bodyBack, newestPhone) || strings.Contains(bodyBack, oldestPhone) {
		t.Fatalf("stepping back did not restore page 1's rows; body:\n%s", bodyBack)
	}
}

// extractHxGetURL finds the hx-get="..." URL on the button whose visible text
// is label, within a small window after the label's first occurrence — good
// enough for this test's fixed, known markup shape
// (outgoing_chat_pager/pager.html both emit `hx-get="URL" ...>Label</button>`
// or `hx-get="URL" ...>Label` with the attribute first).
func extractHxGetURL(t *testing.T, body, label string) string {
	t.Helper()
	marker := ">" + label + "<"
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("could not find a %q button in body:\n%s", label, body)
	}
	// Walk backwards from the label to the start of its <button ...> tag.
	tagStart := strings.LastIndex(body[:idx], "<button")
	if tagStart < 0 {
		t.Fatalf("could not find the opening <button for %q", label)
	}
	tag := body[tagStart:idx]
	const attr = `hx-get="`
	aStart := strings.Index(tag, attr)
	if aStart < 0 {
		t.Fatalf("button for %q has no hx-get attribute: %s", label, tag)
	}
	aStart += len(attr)
	aEnd := strings.Index(tag[aStart:], `"`)
	if aEnd < 0 {
		t.Fatalf("unterminated hx-get attribute for %q: %s", label, tag)
	}
	// The URL was rendered inside an HTML attribute, so "&" between query
	// params comes out as "&amp;" — unescape it back to a real URL.
	return html.UnescapeString(tag[aStart : aStart+aEnd])
}

// extractBetween returns the substring strictly between the first occurrence
// of start and the following occurrence of end.
func extractBetween(t *testing.T, body, start, end string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("could not find %q in body:\n%s", start, body)
	}
	rest := body[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("could not find %q after %q in body:\n%s", end, start, body)
	}
	return rest[:j]
}

// TestOutgoingChatSummaryTablePagesInMemorySessions is the red-without-the-
// change test for item 3's summary table: before this change EVERY session
// rendered on one page with no pager at all. Seeds
// outgoingSummaryPageSize+2 sessions so there are exactly two pages, and
// checks the pager component (the same one /admin/api-clients uses) actually
// slices .Summary server-side.
func TestOutgoingChatSummaryTablePagesInMemorySessions(t *testing.T) {
	srv, sessions, _, _, manager := newOutgoingChatTestServer(t, 1.0, 3)
	ctx := context.Background()
	total := outgoingSummaryPageSize + 2
	var names []string
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("Gateway S%02d", i)
		if _, err := manager.Create(ctx, name, fmt.Sprintf("62800%04d", i)); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}

	page1 := authedGet(t, srv, sessions, "/outgoing-chat")
	body1 := page1.Body.String()
	if !strings.Contains(body1, fmt.Sprintf("(%d)", total)) {
		t.Fatalf("summary button does not show the TOTAL session count %d; body:\n%s", total, body1)
	}
	// The chart plots EVERY session regardless of the summary table's page
	// (it is its own independent popup), so a plain body-wide substring
	// count would also match names inside the chart's JSON blob — scope the
	// count to the summary dialog's own markup.
	table1 := extractBetween(t, body1, `id="modal-outgoing-summary"`, "</dialog>")
	shown1 := 0
	for _, n := range names {
		if strings.Contains(table1, n) {
			shown1++
		}
	}
	if shown1 != outgoingSummaryPageSize {
		t.Fatalf("page 1 of the summary table shows %d sessions, want exactly %d (page size); table markup:\n%s",
			shown1, outgoingSummaryPageSize, table1)
	}

	page2 := authedGet(t, srv, sessions, "/outgoing-chat?page=2")
	body2 := page2.Body.String()
	table2 := extractBetween(t, body2, `id="modal-outgoing-summary"`, "</dialog>")
	shown2 := 0
	for _, n := range names {
		if strings.Contains(table2, n) {
			shown2++
		}
	}
	if shown2 != total-outgoingSummaryPageSize {
		t.Fatalf("page 2 of the summary table shows %d sessions, want %d; table markup:\n%s",
			shown2, total-outgoingSummaryPageSize, table2)
	}
}

// TestOutgoingGapChartXAxisIsScaledToAvoidOverlappingTickLabels is item 2's
// red-without-the-change test: before this change a session averaging 15
// minutes between sends produced a raw-millisecond xAxis ("avg gap (ms)",
// values like 900000) whose 6-7 digit tick labels overlapped on a phone-width
// chart. This asserts the rendered option JSON directly rather than a
// screenshot, per outgoing_chat.go's outgoingGapOption doc comment: the
// option is what actually controls the rendering, and it is fully
// server-computed.
func TestOutgoingGapChartXAxisIsScaledToAvoidOverlappingTickLabels(t *testing.T) {
	rows := []outgoingSessionView{
		{Name: "Gateway Idle", gapAvgMS: 900_000}, // 15 minutes
	}
	raw := outgoingGapOption(rows, time.Second)

	var opt struct {
		XAxis struct {
			Name         string  `json:"name"`
			NameLocation string  `json:"nameLocation"`
			NameGap      float64 `json:"nameGap"`
			AxisLabel    struct {
				HideOverlap bool `json:"hideOverlap"`
			} `json:"axisLabel"`
		} `json:"xAxis"`
		Grid struct {
			Bottom float64 `json:"bottom"`
		} `json:"grid"`
		Series []struct {
			Data []float64 `json:"data"`
		} `json:"series"`
	}
	if err := json.Unmarshal([]byte(raw), &opt); err != nil {
		t.Fatalf("outgoingGapOption produced invalid JSON: %v\n%s", err, raw)
	}

	if opt.XAxis.Name != "avg gap (m)" {
		t.Fatalf("xAxis.name = %q, want \"avg gap (m)\" for a 900000ms value (900000ms=15m, scaled by gapAxisUnit); full option:\n%s",
			opt.XAxis.Name, raw)
	}
	if opt.XAxis.NameLocation != "middle" || opt.XAxis.NameGap <= 0 {
		t.Fatalf("xAxis.name has no explicit nameLocation/nameGap, so it defaults to a position that collides with the last tick label; option:\n%s", raw)
	}
	if !opt.XAxis.AxisLabel.HideOverlap {
		t.Fatalf("xAxis.axisLabel.hideOverlap is not set; option:\n%s", raw)
	}
	if opt.Grid.Bottom < 56 {
		t.Fatalf("grid.bottom = %v, too small to fit a nameLocation:\"middle\" axis name below the tick labels; option:\n%s", opt.Grid.Bottom, raw)
	}
	if len(opt.Series) != 1 || len(opt.Series[0].Data) != 1 {
		t.Fatalf("expected exactly one bar; option:\n%s", raw)
	}
	if got := opt.Series[0].Data[0]; got != 15 {
		t.Fatalf("900000ms should scale to 15 (minutes), got %v; a raw millisecond value here is exactly the overlap bug — option:\n%s", got, raw)
	}
	if strings.Contains(string(raw), "900000") {
		t.Fatalf("option JSON still carries the raw millisecond value 900000 — it must be pre-scaled server-side, not left for a client-side formatter that JSON cannot carry:\n%s", raw)
	}
}

// TestOutgoingGapChartStaysInMillisecondsForSmallGaps pins the OTHER end of
// gapAxisUnit: a small gap must not be needlessly scaled into fractional
// seconds.
func TestOutgoingGapChartStaysInMillisecondsForSmallGaps(t *testing.T) {
	rows := []outgoingSessionView{{Name: "Gateway Fast", gapAvgMS: 250}}
	raw := outgoingGapOption(rows, 300*time.Millisecond)
	if !strings.Contains(string(raw), `"avg gap (ms)"`) {
		t.Fatalf("a 250ms average (floor 300ms) should stay in \"avg gap (ms)\"; option:\n%s", raw)
	}
}

// TestOutgoingChatFilterIsACollapsedDetailsElementAtTheTopOfThePage is item
// 1's markup assertion, in the spirit of nav_mobile_test.go/responsive_test.go
// ("markup the layout depends on is asserted"): the Filter section must be a
// <details> (collapsible, default CLOSED — no `open` attribute) and it must
// appear before the "Ringkasan per Sesi" section, not after the chart like
// it used to.
func TestOutgoingChatFilterIsACollapsedDetailsElementAtTheTopOfThePage(t *testing.T) {
	srv, sessions, _, _, _ := newOutgoingChatTestServer(t, 1.0, 3)
	body := authedGet(t, srv, sessions, "/outgoing-chat").Body.String()

	if !strings.Contains(body, `<details class="filter-details">`) {
		t.Fatalf("Filter is not a <details class=\"filter-details\"> element; body:\n%s", body)
	}
	if strings.Contains(body, `<details class="filter-details" open>`) ||
		strings.Contains(body, `<details open class="filter-details">`) {
		t.Fatalf("Filter <details> must default CLOSED (no open attribute); body:\n%s", body)
	}
	filterIdx := strings.Index(body, `<details class="filter-details">`)
	summaryIdx := strings.Index(body, "Ringkasan per Sesi")
	if filterIdx < 0 || summaryIdx < 0 || filterIdx > summaryIdx {
		t.Fatalf("Filter must render BEFORE \"Ringkasan per Sesi\", not after the chart like before; filterIdx=%d summaryIdx=%d",
			filterIdx, summaryIdx)
	}
}

// TestOutgoingChatSummaryAndChartAreBehindModalTriggersNotInlineOnThePage is
// item 5's markup assertion: both the summary table and the gap chart must
// be reachable only via a [data-modal-open] button into a <dialog>, and
// modal.js must be wired sitewide (base.html) to drive it.
func TestOutgoingChatSummaryAndChartAreBehindModalTriggersNotInlineOnThePage(t *testing.T) {
	srv, sessions, _, _, _ := newOutgoingChatTestServer(t, 1.0, 3)
	body := authedGet(t, srv, sessions, "/outgoing-chat").Body.String()

	for _, want := range []string{
		`data-modal-open="modal-outgoing-summary"`,
		`<dialog id="modal-outgoing-summary" class="modal"`,
		`data-modal-open="modal-outgoing-gap"`,
		`<dialog id="modal-outgoing-gap" class="modal"`,
		`/static/js/modal.js`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("/outgoing-chat missing %q; body:\n%s", want, body)
		}
	}
}

// TestOutgoingChatTablesUseTheCompactRowSpacingModifier is item 4's markup
// assertion: the summary and detail tables must opt into .table-compact
// (datatable.css) rather than the global 12px/16px .table td rule
// (forms.css) — see datatable.css's own comment for why the global rule
// itself is never edited.
func TestOutgoingChatTablesUseTheCompactRowSpacingModifier(t *testing.T) {
	srv, sessions, _, _, _ := newOutgoingChatTestServer(t, 1.0, 3)
	body := authedGet(t, srv, sessions, "/outgoing-chat").Body.String()

	if got := strings.Count(body, `class="table table-wide table-compact"`); got != 2 {
		t.Fatalf("expected exactly 2 tables carrying table-compact (summary + detail), got %d; body:\n%s", got, body)
	}
}
