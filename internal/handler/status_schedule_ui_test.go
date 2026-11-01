package handler

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/contact"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/storage"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

type scheduleHarness struct {
	srv      http.Handler
	sessions *auth.Store
	session  *wa.ManagedSession
	repo     *sched.Repository
	log      *callLog
}

// newStatusScheduleTestServer wires the pieces /status/schedules needs: the
// schedule repository, three contacts, and (when withObjects) a fake object
// store whose put/delete calls are recorded.
func newStatusScheduleTestServer(t *testing.T, withObjects bool) *scheduleHarness {
	t.Helper()
	log := &callLog{}
	if !withObjects {
		return newStatusScheduleTestServerWith(t, nil, log)
	}
	return newStatusScheduleTestServerWith(t, &fakeObjectStore{log: log}, log)
}

func newStatusScheduleTestServerWith(t *testing.T, objects storage.ObjectStore, log *callLog, opts ...func(*Deps)) *scheduleHarness {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	webFS, _ := fs.Sub(wsm.FS, "web")
	rend, err := renderer.New(webFS, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStore(time.Hour)
	t.Cleanup(sessions.Close)
	msgRepo := message.NewRepository(pool)
	mgr := wa.NewManager(ctx, whatsmeowShapedBackend(), wasession.NewRepository(pool), msgRepo, nil,
		wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	s, err := mgr.Create(ctx, "gw", "+62 812-0000-9999")
	if err != nil {
		t.Fatal(err)
	}
	contacts := contact.NewRepository(pool)
	for i, n := range []string{"Ria", "Budi", "Sari"} {
		ph := "62811000000" + string(rune('1'+i))
		if err := contacts.Insert(ctx, &contact.Contact{ID: "c" + ph, Name: n, Phone: ph}); err != nil {
			t.Fatal(err)
		}
	}
	repo := sched.NewRepository(pool)
	d := Deps{
		Renderer: rend, Sessions: sessions, Middleware: auth.NewMiddleware(sessions, apiclient.NewRepository(pool), ""),
		Manager: mgr, Messages: msgRepo, MessageStats: msgRepo, APIClients: apiclient.NewRepository(pool),
		Contacts: contacts, StatusSchedules: repo,
		MediaMaxBytes: 4 << 20, MediaUploadTimeout: 5 * time.Second, MediaUploadConcurrency: 2,
		SessionTTL: time.Hour, AssetsFS: wsm.FS,
	}
	d.Objects = objects
	for _, o := range opts {
		o(&d)
	}
	return &scheduleHarness{srv: New(d), sessions: sessions, session: s, repo: repo, log: log}
}

func (h *scheduleHarness) validForm() url.Values {
	return url.Values{
		"name": {"Morning"}, "session_id": {h.session.ID}, "kind": {"text"}, "text": {"Good morning"},
		"background_argb": {"4280184399"}, "weekday": {"1", "3"},
		"window_start": {"07:00"}, "window_end": {"10:27"}, "timezone": {"Asia/Jakarta"},
		"start_date": {"2026-11-02"},
	}
}

func (h *scheduleHarness) onlySchedule(t *testing.T) sched.Schedule {
	t.Helper()
	list, err := h.repo.List(context.Background(), 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("schedules = %v (err %v), want exactly one", list, err)
	}
	return list[0]
}

var jpegMagic = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1}

func TestTheNewScheduleFormRendersEveryContactCheckedByDefault(t *testing.T) {
	h := newStatusScheduleTestServer(t, false)
	res := authedGet(t, h.srv, h.sessions, "/status/schedules/new")
	if res.Code != http.StatusOK {
		t.Fatalf("GET /status/schedules/new = %d", res.Code)
	}
	body := res.Body.String()
	boxes := regexp.MustCompile(`<input type="checkbox" name="recipients"[^>]*>`).FindAllString(body, -1)
	if len(boxes) != 3 {
		t.Fatalf("recipient checkboxes = %d, want one per contact (3)", len(boxes))
	}
	for _, b := range boxes {
		if !strings.Contains(b, " checked") {
			t.Errorf("a contact checkbox is NOT checked by default: %s", b)
		}
	}
	for _, want := range []string{"data-status-select-all", "data-status-select-none"} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks the %s control", want)
		}
	}
}

func TestAllCheckedPersistsAsModeAllWithNoFrozenList(t *testing.T) {
	h := newStatusScheduleTestServer(t, false)
	form := h.validForm()
	form["recipients"] = []string{"628110000001@s.whatsapp.net", "628110000002@s.whatsapp.net", "628110000003@s.whatsapp.net"}
	res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", form)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d, want 303; body:\n%s", res.Code, res.Body.String())
	}
	sc := h.onlySchedule(t)
	if sc.RecipientsMode != sched.ModeAll || sc.RecipientsJSON != "" {
		t.Errorf("mode=%q json=%q, want all with no frozen list", sc.RecipientsMode, sc.RecipientsJSON)
	}
	if sc.State != sched.StateActive || sc.Weekdays != "1,3" || sc.WindowEnd != "10:27" || sc.SessionID != h.session.ID {
		t.Errorf("stored schedule wrong: %+v", sc)
	}
}

func TestAPartialSelectionPersistsAsACustomList(t *testing.T) {
	h := newStatusScheduleTestServer(t, false)
	form := h.validForm()
	form["recipients"] = []string{"628110000001@s.whatsapp.net", "628110000003@s.whatsapp.net"}
	if res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", form); res.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d: %s", res.Code, res.Body.String())
	}
	sc := h.onlySchedule(t)
	if sc.RecipientsMode != sched.ModeCustom || !strings.Contains(sc.RecipientsJSON, "628110000001") ||
		strings.Contains(sc.RecipientsJSON, "628110000002") {
		t.Errorf("mode=%q json=%q", sc.RecipientsMode, sc.RecipientsJSON)
	}
}

func TestEditingACustomScheduleTicksOnlyItsRecipientsAndAnAllScheduleTicksEveryone(t *testing.T) {
	h := newStatusScheduleTestServer(t, false)
	ctx := context.Background()
	base := sched.Schedule{Name: "n", SessionID: h.session.ID, Kind: "text", Text: "t", Weekdays: "1", WindowStart: "07:00",
		WindowEnd: "08:00", Timezone: "UTC", StartDate: "2026-11-02", State: sched.StateActive}
	custom, all := base, base
	custom.ID, custom.RecipientsMode, custom.RecipientsJSON = "custom", sched.ModeCustom, `["628110000002@s.whatsapp.net"]`
	all.ID, all.RecipientsMode = "all", sched.ModeAll
	for _, s := range []*sched.Schedule{&custom, &all} {
		if err := h.repo.Insert(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	count := func(id string) (checked, total int) {
		body := authedGet(t, h.srv, h.sessions, "/status/schedules/"+id+"/edit").Body.String()
		for _, b := range regexp.MustCompile(`<input type="checkbox" name="recipients"[^>]*>`).FindAllString(body, -1) {
			total++
			if strings.Contains(b, " checked") {
				checked++
			}
		}
		return
	}
	if c, n := count("custom"); c != 1 || n != 3 {
		t.Errorf("custom edit form: %d of %d checked, want 1 of 3", c, n)
	}
	if c, n := count("all"); c != 3 || n != 3 {
		t.Errorf("all-mode edit form: %d of %d checked, want 3 of 3", c, n)
	}
}

func TestScheduleValidationRejectsBadInputWithoutSaving(t *testing.T) {
	h := newStatusScheduleTestServer(t, false)
	allRecips := []string{"628110000001@s.whatsapp.net"}
	cases := []struct {
		name, want string
		mut        func(url.Values)
	}{
		{"no name", "name is required", func(v url.Values) { v.Set("name", "") }},
		{"unknown session", "unknown session", func(v url.Values) { v.Set("session_id", "nope") }},
		{"no weekday", "tick at least one weekday", func(v url.Values) { v.Del("weekday") }},
		{"end before start", "window end must be after window start", func(v url.Values) { v.Set("window_end", "06:00") }},
		{"bad time", "window start must be a valid HH:MM time", func(v url.Values) { v.Set("window_start", "7am") }},
		{"bad tz", "unknown timezone", func(v url.Values) { v.Set("timezone", "Mars/Base") }},
		{"end date before start", "end date must not be before start date", func(v url.Values) { v.Set("end_date", "2026-10-01") }},
		{"no text", "status text is required", func(v url.Values) { v.Set("text", " ") }},
		{"no recipients", "tick at least one recipient", func(v url.Values) { v.Del("recipients") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			form := h.validForm()
			form["recipients"] = allRecips
			c.mut(form)
			res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", form)
			if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), c.want) {
				t.Fatalf("POST = %d, want a 200 re-render containing %q; body:\n%s", res.Code, c.want, res.Body.String())
			}
		})
	}
	if list, _ := h.repo.List(context.Background(), 10); len(list) != 0 {
		t.Errorf("%d schedule(s) saved from invalid submissions", len(list))
	}
}

func TestAnImageScheduleIsRefusedWhenThereIsNoObjectStorage(t *testing.T) {
	h := newStatusScheduleTestServer(t, false)
	res := postMultipartAsAdmin(t, h.srv, h.sessions, "/status/schedules", scheduleFields(h, "image"), "file_image", "a.jpg", jpegMagic)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "need object storage") {
		t.Fatalf("POST = %d, want a 200 re-render naming object storage; body:\n%s", res.Code, res.Body.String())
	}
	if list, _ := h.repo.List(context.Background(), 10); len(list) != 0 {
		t.Error("an image schedule was saved without object storage")
	}
}

func scheduleFields(h *scheduleHarness, kind string) []struct{ key, value string } {
	return []struct{ key, value string }{
		{"name", "Pic"}, {"session_id", h.session.ID}, {"kind", kind}, {"weekday", "1"},
		{"window_start", "07:00"}, {"window_end", "10:27"}, {"timezone", "Asia/Jakarta"}, {"start_date", "2026-11-02"},
		{"recipients", "628110000001@s.whatsapp.net"}, {"caption", "cap"},
	}
}

func TestAnImageScheduleStoresItsFileHardAtSaveTime(t *testing.T) {
	h := newStatusScheduleTestServer(t, true)
	res := postMultipartAsAdmin(t, h.srv, h.sessions, "/status/schedules", scheduleFields(h, "image"), "file_image", "a.jpg", jpegMagic)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d; body:\n%s", res.Code, res.Body.String())
	}
	sc := h.onlySchedule(t)
	if sc.Kind != sched.KindImage || sc.MediaObjectKey == "" || sc.MediaContentType != "image/jpeg" || sc.Caption != "cap" {
		t.Errorf("stored schedule: %+v", sc)
	}
	if calls := strings.Join(h.log.snapshot(), ","); !strings.Contains(calls, "put:"+sc.MediaObjectKey) {
		t.Errorf("object store calls = %q, want a put of the stored key", calls)
	}
}

func TestAnImageScheduleWithAFailedObjectPutIsNotSaved(t *testing.T) {
	log := &callLog{}
	h := newStatusScheduleTestServerWith(t, &fakeObjectStore{log: log, putErr: context.DeadlineExceeded}, log)
	res := postMultipartAsAdmin(t, h.srv, h.sessions, "/status/schedules", scheduleFields(h, "image"), "file_image", "a.jpg", jpegMagic)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "could not store the media file") {
		t.Fatalf("POST = %d, want the hard-failure message; body:\n%s", res.Code, res.Body.String())
	}
	if list, _ := h.repo.List(context.Background(), 10); len(list) != 0 {
		t.Error("a schedule was saved although its media never reached storage")
	}
}

func TestAnImageScheduleRejectsBytesThatAreNotAnImage(t *testing.T) {
	h := newStatusScheduleTestServer(t, true)
	res := postMultipartAsAdmin(t, h.srv, h.sessions, "/status/schedules", scheduleFields(h, "image"), "file_image", "a.jpg", []byte("just text, not a picture"))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "does not look like a image") {
		t.Fatalf("POST = %d; body:\n%s", res.Code, res.Body.String())
	}
}

func TestPauseResumeAndDeleteMoveTheScheduleAndDeleteRemovesItsMedia(t *testing.T) {
	h := newStatusScheduleTestServer(t, true)
	if res := postMultipartAsAdmin(t, h.srv, h.sessions, "/status/schedules", scheduleFields(h, "video"), "file_video", "v.mp4",
		[]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0, 'i', 's', 'o', 'm', 'm', 'p', '4', '1'}); res.Code != http.StatusSeeOther {
		t.Fatalf("create = %d: %s", res.Code, res.Body.String())
	}
	sc := h.onlySchedule(t)
	state := func() string { s, _ := h.repo.Get(context.Background(), sc.ID); return s.State }

	if res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules/"+sc.ID+"/pause", nil); res.Code != http.StatusOK || state() != sched.StatePaused {
		t.Fatalf("pause = %d state=%s", res.Code, state())
	} else if !strings.Contains(res.Body.String(), "/resume") {
		t.Errorf("the paused row does not offer Resume:\n%s", res.Body.String())
	}
	if res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules/"+sc.ID+"/resume", nil); res.Code != http.StatusOK || state() != sched.StateActive {
		t.Fatalf("resume = %d state=%s", res.Code, state())
	}
	if res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules/"+sc.ID+"/resume", nil); res.Code != http.StatusOK ||
		!strings.Contains(res.Body.String(), "cannot resume") {
		t.Errorf("resuming an active schedule must report an error row; got %d %s", res.Code, res.Body.String())
	}
	res := postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules/"+sc.ID+"/delete", nil)
	if res.Code != http.StatusOK || strings.TrimSpace(res.Body.String()) != "" {
		t.Fatalf("delete = %d body %q, want an empty 200 so htmx removes the row", res.Code, res.Body.String())
	}
	if _, err := h.repo.Get(context.Background(), sc.ID); err == nil {
		t.Error("schedule still exists after delete")
	}
	if calls := strings.Join(h.log.snapshot(), ","); !strings.Contains(calls, "delete:"+sc.MediaObjectKey) {
		t.Errorf("object store calls = %q, want the stored media deleted with the schedule", calls)
	}
}

func TestEditKeepsTheStoredFileWhenNoneIsUploadedAndDeletesItWhenSwitchingToText(t *testing.T) {
	h := newStatusScheduleTestServer(t, true)
	postMultipartAsAdmin(t, h.srv, h.sessions, "/status/schedules", scheduleFields(h, "image"), "file_image", "a.jpg", jpegMagic)
	sc := h.onlySchedule(t)

	// Same kind, no new file: the stored key survives.
	if res := postMultipartAsAdmin(t, h.srv, h.sessions, "/status/schedules/"+sc.ID, scheduleFields(h, "image"), "", "", nil); res.Code != http.StatusSeeOther {
		t.Fatalf("edit = %d: %s", res.Code, res.Body.String())
	}
	if got, _ := h.repo.Get(context.Background(), sc.ID); got.MediaObjectKey != sc.MediaObjectKey {
		t.Errorf("key changed on a no-file edit: %q -> %q", sc.MediaObjectKey, got.MediaObjectKey)
	}

	// Switch to text: valid, and the orphaned object is removed.
	fields := append(scheduleFields(h, "text"), struct{ key, value string }{"text", "now text"})
	if res := postMultipartAsAdmin(t, h.srv, h.sessions, "/status/schedules/"+sc.ID, fields, "", "", nil); res.Code != http.StatusSeeOther {
		t.Fatalf("edit to text = %d: %s", res.Code, res.Body.String())
	}
	if got, _ := h.repo.Get(context.Background(), sc.ID); got.MediaObjectKey != "" || got.Kind != sched.KindText {
		t.Errorf("after switching to text: %+v", got)
	}
	if calls := strings.Join(h.log.snapshot(), ","); !strings.Contains(calls, "delete:"+sc.MediaObjectKey) {
		t.Errorf("object store calls = %q, want the old file deleted", calls)
	}
}

func TestTheListPageAndRowsPartialShowASavedSchedule(t *testing.T) {
	h := newStatusScheduleTestServer(t, false)
	form := h.validForm()
	form["recipients"] = []string{"628110000001@s.whatsapp.net", "628110000002@s.whatsapp.net", "628110000003@s.whatsapp.net"}
	postFormAsAdmin(t, h.srv, h.sessions, "/status/schedules", form)

	page := authedGet(t, h.srv, h.sessions, "/status/schedules")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `hx-get="/status/schedules/rows"`) {
		t.Fatalf("list page = %d\n%s", page.Code, page.Body.String())
	}
	rows := authedGet(t, h.srv, h.sessions, "/status/schedules/rows").Body.String()
	for _, want := range []string{"Morning", "Mon, Wed", "07:00–10:27 Asia/Jakarta", "All contacts", "badge-active"} {
		if !strings.Contains(rows, want) {
			t.Errorf("rows partial lacks %q:\n%s", want, rows)
		}
	}
}

func TestStatusSchedulePagesRequireTheAdminCookie(t *testing.T) {
	h := newStatusScheduleTestServer(t, false)
	for _, p := range []string{"/status/schedules", "/status/schedules/new", "/status/schedules/rows"} {
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s answered 200 without a session cookie", p)
		}
	}
}

func TestTheSummarySentenceNamesTheDaysWindowAndZone(t *testing.T) {
	got := statusScheduleSummary([]string{"3", "1"}, "07:00", "10:27", "Asia/Jakarta", false)
	if got != "Mon, Wed · once each day, at a random time between 07:00 and 10:27 Asia/Jakarta" {
		t.Errorf("summary = %q", got)
	}
	if got := statusScheduleSummary(nil, "07:00", "10:27", "UTC", false); !strings.Contains(got, "at least one weekday") {
		t.Errorf("empty selection summary = %q", got)
	}
}

func TestTheStatusPageAndNavLinkToTheSchedules(t *testing.T) {
	h := newStatusScheduleTestServer(t, false)
	body := authedGet(t, h.srv, h.sessions, "/status").Body.String()
	if strings.Count(body, `href="/status/schedules"`) < 2 {
		t.Errorf("/status should link to /status/schedules from the page and the sidebar; found %d", strings.Count(body, `href="/status/schedules"`))
	}
}

var _ storage.ObjectStore = (*fakeObjectStore)(nil)
