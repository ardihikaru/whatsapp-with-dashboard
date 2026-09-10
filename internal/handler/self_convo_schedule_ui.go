// self_convo_schedule_ui.go: the operator UI for Convo scheduling — turning
// a persisted convo.Schedule (internal/domain/convo/schedule_model.go) into
// pages and HTMX partials. Follows self_convo_ui.go's/
// self_convo_templates_ui.go's house shape throughout: the rows table and
// its row actions are HTMX partials (convo_schedule_rows.html) rendered at
// 2xx no matter the outcome (htmx only swaps 2xx), while the multi-field
// create/edit form is a plain full-page POST that re-renders itself with
// Form+Error on a rejection — api_clients.go's shape, not an htmx swap,
// because this form is too large to live inside a table row.
//
// No new domain/runner logic lives here: every mutation goes through
// convo.Repository (CRUD/listing) or *selfconvo.Planner (Pause/Resume/
// Stop_, already committed) exactly as the phase brief requires.
package handler

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/convo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// scheduleDisabledMessage mirrors selfConvoDisabledMessage (self_convo_ui.go)
// for schedule mutations: a schedule that reaches `active` sends messages on
// its own, indefinitely, which is exactly the automated-traffic hazard
// SelfConvoEnabled exists to gate — see that constant's own doc comment.
// Reading a schedule (list/rows/view) is never gated, the same "management
// works, sending does not" split self_convo_templates_ui.go documents for
// template CRUD.
const scheduleDisabledMessage = "Convo scheduling is disabled on this deployment. Set WSM_SELF_CONVO_ENABLED=true and restart to enable it."

// scheduleTimezones is the curated IANA list the create/edit form's
// timezone <select> offers. Server-side validation (validateScheduleForm)
// accepts ANY zone time.LoadLocation resolves, not only these — this list
// is a UI convenience, not the whole of what is legal.
var scheduleTimezones = []string{
	"Asia/Jakarta",
	"Asia/Makassar",
	"Asia/Jayapura",
	"Asia/Singapore",
	"Asia/Kuala_Lumpur",
	"UTC",
}

// defaultScheduleTimezone matches Planner's own operational default market
// (CLAUDE.md's Convo scheduling notes) and is what a blank/new form starts
// with.
const defaultScheduleTimezone = "Asia/Jakarta"

// weekdayLabels is Monday..Sunday, index 0..6, matching planner.go's own
// ISO-8601 numbering convention (Monday=1..Sunday=7 — scheduleFiresOn's doc
// comment) one-for-one: weekdayLabels[i] is the label for value i+1.
var weekdayLabels = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

// weekdayShortNames is weekdayLabels keyed by the stored string form, for
// rendering a schedule's Weekdays column back out as names.
var weekdayShortNames = map[string]string{
	"1": "Mon", "2": "Tue", "3": "Wed", "4": "Thu", "5": "Fri", "6": "Sat", "7": "Sun",
}

// weekdayOption is one checkbox of the weekly recurrence's Mon..Sun row.
type weekdayOption struct {
	Value   string
	Label   string
	Checked bool
}

// weekdayOptions builds all seven checkboxes, marking Checked from a
// comma-joined selection (the same Weekdays column shape
// scheduleFiresOn/planner.go reads).
func weekdayOptions(selectedCSV string) []weekdayOption {
	set := map[string]bool{}
	for _, s := range strings.Split(selectedCSV, ",") {
		if s = strings.TrimSpace(s); s != "" {
			set[s] = true
		}
	}
	out := make([]weekdayOption, 7)
	for i := 0; i < 7; i++ {
		v := strconv.Itoa(i + 1)
		out[i] = weekdayOption{Value: v, Label: weekdayLabels[i], Checked: set[v]}
	}
	return out
}

// scheduleForm is the raw create/edit form, kept as strings (api_clients.go's
// apiClientForm convention) so a rejected submission re-renders exactly as
// typed rather than silently reverting fields to zero values.
type scheduleForm struct {
	Name           string
	SenderID       string
	ReceiverIDs    []string
	Topic          string
	RootTemplateID string
	ChainEnabled   bool
	Recurrence     string
	Weekdays       []string // raw ticked values, e.g. ["1","3"]
	IntervalDays   string
	SetsPerDay     string
	WindowStart    string
	WindowEnd      string
	Timezone       string
	StartDate      string
	EndDate        string
}

// newScheduleForm is the blank create form's starting point: sane defaults
// so an operator filling in the common case (daily, one set, business
// hours, Jakarta, starting today, no end date) barely has to touch
// anything.
func newScheduleForm(now time.Time) scheduleForm {
	return scheduleForm{
		ChainEnabled: true,
		Recurrence:   convo.RecurrenceDaily,
		SetsPerDay:   "1",
		WindowStart:  "09:00",
		WindowEnd:    "17:00",
		Timezone:     defaultScheduleTimezone,
		StartDate:    now.Format(dateInputLayout),
	}
}

// dateInputLayout is the <input type="date"> value shape, matching
// dateLayout in internal/selfconvo/planner.go (kept as a separate literal —
// this package has no reason to depend on that one).
const dateInputLayout = "2006-01-02"

// scheduleFormFromSchedule rebuilds the edit form's starting values from a
// persisted schedule and its receivers.
func scheduleFormFromSchedule(sc convo.Schedule, receivers []convo.ScheduleReceiver) scheduleForm {
	ids := make([]string, len(receivers))
	for i, rec := range receivers {
		ids[i] = rec.SessionID
	}
	var weekdays []string
	if sc.Weekdays != "" {
		weekdays = strings.Split(sc.Weekdays, ",")
	}
	intervalDays := ""
	if sc.IntervalDays > 0 {
		intervalDays = strconv.Itoa(sc.IntervalDays)
	}
	return scheduleForm{
		Name:           sc.Name,
		SenderID:       sc.SenderSessionID,
		ReceiverIDs:    ids,
		Topic:          sc.Topic,
		RootTemplateID: sc.RootTemplateID,
		ChainEnabled:   sc.ChainEnabled,
		Recurrence:     sc.Recurrence,
		Weekdays:       weekdays,
		IntervalDays:   intervalDays,
		SetsPerDay:     strconv.Itoa(sc.SetsPerDay),
		WindowStart:    sc.WindowStart,
		WindowEnd:      sc.WindowEnd,
		Timezone:       sc.Timezone,
		StartDate:      sc.StartDate,
		EndDate:        sc.EndDate,
	}
}

// scheduleFormFromRequest reads a POSTed scheduleForm. The caller must
// already have called r.ParseForm.
func scheduleFormFromRequest(r *http.Request) scheduleForm {
	return scheduleForm{
		Name:           strings.TrimSpace(r.FormValue("name")),
		SenderID:       strings.TrimSpace(r.FormValue("sender_id")),
		ReceiverIDs:    dedupeStrings(r.Form["receiver_id"]),
		Topic:          strings.TrimSpace(r.FormValue("topic")),
		RootTemplateID: strings.TrimSpace(r.FormValue("root_template_id")),
		ChainEnabled:   r.FormValue("chain_enabled") != "",
		Recurrence:     strings.TrimSpace(r.FormValue("recurrence")),
		Weekdays:       r.Form["weekday"],
		IntervalDays:   strings.TrimSpace(r.FormValue("interval_days")),
		SetsPerDay:     strings.TrimSpace(r.FormValue("sets_per_day")),
		WindowStart:    strings.TrimSpace(r.FormValue("window_start")),
		WindowEnd:      strings.TrimSpace(r.FormValue("window_end")),
		Timezone:       strings.TrimSpace(r.FormValue("timezone")),
		StartDate:      strings.TrimSpace(r.FormValue("start_date")),
		EndDate:        strings.TrimSpace(r.FormValue("end_date")),
	}
}

// scheduleFormPageData drives GET/POST .../schedules/new and .../{id}/edit —
// one page, one pageDef ("self_convo_schedule_form"), IsEdit distinguishes
// the two only for the heading/submit-label/form-action, same shape as
// api_clients.go's edit vs create split but sharing one template instead of
// two.
type scheduleFormPageData struct {
	PageBase
	Enabled     bool
	IsEdit      bool
	ScheduleID  string
	Form        scheduleForm
	Error       string
	Sessions    []wa.Snapshot
	Topics      []convo.TopicSummary
	Timezones   []string
	Weekdays    []weekdayOption
	ReceiverSet map[string]bool
}

// scheduleSessions is the sender/receiver picker's source — deliberately
// the SAME connectedSnapshots(h.Manager.Snapshots()) call trigger.html's
// own participant picker uses (self_convo_ui.go's GetSelfConvo), so an
// operator sees one consistent notion of "which sessions can take part in
// something" across both surfaces.
func (h *handlers) scheduleSessions() []wa.Snapshot {
	return connectedSnapshots(h.Manager.Snapshots())
}

// scheduleTopics is the topic <select>'s data source. h.Convo == nil (older
// test servers) renders no options, the same nil-guard self_convo_ui.go's
// selfConvoTemplates uses.
func (h *handlers) scheduleTopics(r *http.Request) []convo.TopicSummary {
	if h.Convo == nil {
		return nil
	}
	topics, err := h.Convo.TopicSummaries(r.Context())
	if err != nil {
		return nil
	}
	return topics
}

// receiverSet turns a form's ticked receiver ids into a lookup the template
// can `index` against — html/template has no "is X in this slice" helper,
// and adding one to the FuncMap (renderer.go) would be visible to every
// page/partial for a need exactly one form has.
func receiverSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func (h *handlers) newScheduleFormPageData(r *http.Request, isEdit bool, scheduleID string, form scheduleForm, errMsg string) scheduleFormPageData {
	data := scheduleFormPageData{
		Enabled: h.SelfConvoEnabled, IsEdit: isEdit, ScheduleID: scheduleID,
		Form: form, Error: errMsg,
		Sessions: h.scheduleSessions(), Topics: h.scheduleTopics(r),
		Timezones: scheduleTimezones, Weekdays: weekdayOptions(strings.Join(form.Weekdays, ",")),
		ReceiverSet: receiverSet(form.ReceiverIDs),
	}
	title := "New Schedule"
	if isEdit {
		title = "Edit Schedule"
	}
	data.PageBase = h.newPageBase(r, title, "/self-convo/schedules")
	return data
}

func (h *handlers) renderScheduleForm(w http.ResponseWriter, r *http.Request, isEdit bool, scheduleID string, form scheduleForm, errMsg string) {
	data := h.newScheduleFormPageData(r, isEdit, scheduleID, form, errMsg)
	h.Renderer.RenderPage(w, "self_convo_schedule_form", data)
}

// GetSelfConvoScheduleNew renders the blank create form. Never gated by
// SelfConvoEnabled — same "the form still renders so you can see how it
// works" rule trigger.html follows; only the POST is refused.
func (h *handlers) GetSelfConvoScheduleNew(w http.ResponseWriter, r *http.Request) {
	h.renderScheduleForm(w, r, false, "", newScheduleForm(time.Now().UTC()), "")
}

// GetSelfConvoScheduleEdit renders id's current settings as the edit form.
func (h *handlers) GetSelfConvoScheduleEdit(w http.ResponseWriter, r *http.Request) {
	if h.Convo == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	sc, err := h.Convo.GetSchedule(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	receivers, err := h.Convo.ScheduleReceivers(r.Context(), id)
	if err != nil {
		receivers = nil
	}
	h.renderScheduleForm(w, r, true, id, scheduleFormFromSchedule(sc, receivers), "")
}

// scheduleInputs is validateScheduleForm's resolved, typed output — every
// field the repository layer actually wants, as opposed to scheduleForm's
// raw strings.
type scheduleInputs struct {
	Name                              string
	SenderID, SenderName, SenderPhone string
	Receivers                         []convo.ScheduleReceiver
	Topic                             string
	RootTemplateID                    string
	ChainEnabled                      bool
	Recurrence                        string
	Weekdays                          string
	IntervalDays                      int
	SetsPerDay                        int
	WindowStart, WindowEnd            string
	Timezone                          string
	StartDate, EndDate                string
}

// validWeekdays keeps only the values 1..7 (isoWeekday's own range,
// planner.go), deduped and sorted ascending regardless of tick order.
func validWeekdays(in []string) []string {
	seen := map[int]bool{}
	for _, s := range in {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 || n > 7 {
			continue
		}
		seen[n] = true
	}
	if len(seen) == 0 {
		return nil
	}
	nums := make([]int, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]string, len(nums))
	for i, n := range nums {
		out[i] = strconv.Itoa(n)
	}
	return out
}

// validScheduleClock reports whether s is "HH:MM" in range — a standalone
// copy of planner.go's own unexported parseClock (that package has no
// reason to export a parser this package can call instead; both check the
// exact same shape). Kept minimal: this is a FORM validity check, not the
// planner's own authority over the value once stored.
func validScheduleClock(s string) bool {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return false
	}
	return true
}

// validateScheduleForm checks every rule the phase brief names, in the
// order an operator would fix them, and returns the FIRST violation's
// message — the same one-message-at-a-time shape apiClientForm.
// tokenSettings (api_clients_form.go) uses. A topic with no opener-stage
// templates reuses selfconvo.ErrNoTemplatesForTopic's own wording verbatim
// (via PickOpener's exact wrapping shape) rather than inventing a second
// phrasing for the same problem — the phase brief's explicit instruction.
func (h *handlers) validateScheduleForm(r *http.Request, form scheduleForm) (scheduleInputs, string) {
	var out scheduleInputs
	ctx := r.Context()

	if form.Name == "" {
		return out, "name is required"
	}
	if rc := len([]rune(form.Name)); rc > selfconvo.MaxNameChars {
		return out, fmt.Sprintf("name is over %d characters", selfconvo.MaxNameChars)
	}
	out.Name = form.Name

	if form.SenderID == "" {
		return out, "choose a sender session"
	}
	sender, ok := h.Manager.Get(form.SenderID)
	if !ok {
		return out, "unknown sender session"
	}
	out.SenderID, out.SenderName, out.SenderPhone = sender.ID, sender.Name, sender.Phone

	receiverIDs := dedupeStrings(form.ReceiverIDs)
	if len(receiverIDs) == 0 {
		return out, "tick at least one receiver"
	}
	for _, rid := range receiverIDs {
		if rid == form.SenderID {
			return out, "the sender cannot also be a receiver"
		}
	}
	receivers := make([]convo.ScheduleReceiver, 0, len(receiverIDs))
	for i, rid := range receiverIDs {
		s, ok := h.Manager.Get(rid)
		if !ok {
			return out, "unknown receiver session"
		}
		receivers = append(receivers, convo.ScheduleReceiver{
			SessionID: s.ID, SessionName: s.Name, Phone: s.Phone, Position: i,
		})
	}
	out.Receivers = receivers

	if form.Topic == "" {
		return out, "choose a topic"
	}
	if h.Convo == nil {
		return out, "self convo is not available on this deployment"
	}
	openers, err := h.Convo.TemplateIDsByTopicStage(ctx, form.Topic, convo.StageOpener, 1)
	if err != nil {
		return out, err.Error()
	}
	if len(openers) == 0 {
		return out, fmt.Errorf("%w: %q", selfconvo.ErrNoTemplatesForTopic, form.Topic).Error()
	}
	out.Topic = form.Topic

	if form.RootTemplateID != "" {
		if _, err := h.Convo.GetTemplate(ctx, form.RootTemplateID); err != nil {
			if errors.Is(err, convo.ErrNotFound) {
				return out, "root template not found"
			}
			return out, err.Error()
		}
	}
	out.RootTemplateID = form.RootTemplateID
	out.ChainEnabled = form.ChainEnabled

	switch form.Recurrence {
	case convo.RecurrenceDaily, convo.RecurrenceWeekly, convo.RecurrenceInterval:
		out.Recurrence = form.Recurrence
	default:
		return out, "unknown recurrence"
	}

	if out.Recurrence == convo.RecurrenceWeekly {
		days := validWeekdays(form.Weekdays)
		if len(days) == 0 {
			return out, "tick at least one weekday for a weekly schedule"
		}
		out.Weekdays = strings.Join(days, ",")
	}

	if out.Recurrence == convo.RecurrenceInterval {
		n, err := strconv.Atoi(form.IntervalDays)
		if err != nil || n < 1 {
			return out, "interval must be at least 1 day"
		}
		out.IntervalDays = n
	}

	setsPerDay, err := strconv.Atoi(form.SetsPerDay)
	if err != nil || setsPerDay < 1 || setsPerDay > 10 {
		return out, "sets per day must be between 1 and 10"
	}
	out.SetsPerDay = setsPerDay

	if !validScheduleClock(form.WindowStart) {
		return out, "window start must be a valid HH:MM time"
	}
	if !validScheduleClock(form.WindowEnd) {
		return out, "window end must be a valid HH:MM time"
	}
	if form.WindowEnd <= form.WindowStart {
		return out, "window end must be after window start"
	}
	out.WindowStart, out.WindowEnd = form.WindowStart, form.WindowEnd

	if _, err := time.LoadLocation(form.Timezone); err != nil {
		return out, fmt.Sprintf("unknown timezone %q", form.Timezone)
	}
	out.Timezone = form.Timezone

	startDate := form.StartDate
	if startDate == "" {
		startDate = time.Now().UTC().Format(dateInputLayout)
	}
	if _, err := time.Parse(dateInputLayout, startDate); err != nil {
		return out, "start date must be a valid date"
	}
	out.StartDate = startDate

	if form.EndDate != "" {
		if _, err := time.Parse(dateInputLayout, form.EndDate); err != nil {
			return out, "end date must be a valid date"
		}
		if form.EndDate < startDate {
			return out, "end date must not be before start date"
		}
	}
	out.EndDate = form.EndDate

	return out, ""
}

// PostSelfConvoScheduleCreate is POST /self-convo/schedules.
func (h *handlers) PostSelfConvoScheduleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderScheduleForm(w, r, false, "", scheduleForm{}, "invalid form submission")
		return
	}
	form := scheduleFormFromRequest(r)
	if h.Convo == nil {
		h.renderScheduleForm(w, r, false, "", form, "self convo is not available on this deployment")
		return
	}
	if !h.SelfConvoEnabled {
		h.renderScheduleForm(w, r, false, "", form, scheduleDisabledMessage)
		return
	}
	in, errMsg := h.validateScheduleForm(r, form)
	if errMsg != "" {
		h.renderScheduleForm(w, r, false, "", form, errMsg)
		return
	}

	sc := &convo.Schedule{
		ID: wa.NewID(), Name: in.Name,
		SenderSessionID: in.SenderID, SenderName: in.SenderName, SenderPhone: in.SenderPhone,
		Topic: in.Topic, RootTemplateID: in.RootTemplateID, ChainEnabled: in.ChainEnabled,
		Recurrence: in.Recurrence, Weekdays: in.Weekdays, IntervalDays: in.IntervalDays,
		SetsPerDay: in.SetsPerDay, WindowStart: in.WindowStart, WindowEnd: in.WindowEnd,
		Timezone: in.Timezone, StartDate: in.StartDate, EndDate: in.EndDate,
		State: convo.ScheduleActive,
	}
	if err := h.Convo.InsertSchedule(r.Context(), sc, in.Receivers); err != nil {
		h.renderScheduleForm(w, r, false, "", form, err.Error())
		return
	}
	http.Redirect(w, r, h.BasePath+"/self-convo/schedules", http.StatusSeeOther)
}

// PostSelfConvoScheduleUpdate is POST /self-convo/schedules/{id}. Fields the
// form does not carry (State/PausedReason/PausedAt/LastPlannedDate/
// CreatedAt) are preserved from the existing row — UpdateSchedule replaces
// the WHOLE row, so this handler must never let a settings edit silently
// resurrect a paused/stalled schedule to active or wipe its planning
// history.
func (h *handlers) PostSelfConvoScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.Convo == nil {
		http.NotFound(w, r)
		return
	}
	existing, err := h.Convo.GetSchedule(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderScheduleForm(w, r, true, id, scheduleForm{}, "invalid form submission")
		return
	}
	form := scheduleFormFromRequest(r)
	if !h.SelfConvoEnabled {
		h.renderScheduleForm(w, r, true, id, form, scheduleDisabledMessage)
		return
	}
	in, errMsg := h.validateScheduleForm(r, form)
	if errMsg != "" {
		h.renderScheduleForm(w, r, true, id, form, errMsg)
		return
	}

	existing.Name = in.Name
	existing.SenderSessionID, existing.SenderName, existing.SenderPhone = in.SenderID, in.SenderName, in.SenderPhone
	existing.Topic = in.Topic
	existing.RootTemplateID = in.RootTemplateID
	existing.ChainEnabled = in.ChainEnabled
	existing.Recurrence = in.Recurrence
	existing.Weekdays = in.Weekdays
	existing.IntervalDays = in.IntervalDays
	existing.SetsPerDay = in.SetsPerDay
	existing.WindowStart, existing.WindowEnd, existing.Timezone = in.WindowStart, in.WindowEnd, in.Timezone
	existing.StartDate, existing.EndDate = in.StartDate, in.EndDate

	if err := h.Convo.UpdateSchedule(r.Context(), &existing, in.Receivers); err != nil {
		h.renderScheduleForm(w, r, true, id, form, err.Error())
		return
	}
	http.Redirect(w, r, h.BasePath+"/self-convo/schedules/"+id, http.StatusSeeOther)
}

// scheduleView wraps a convo.Schedule with the presentation fields
// convo_schedule_rows.html/schedule_view.html need — toConvoRunView's
// (self_convo_ui.go) sibling.
type scheduleView struct {
	convo.Schedule
	ReceiverCount   int
	RecurrenceLabel string
	WindowLabel     string
	NoEndDate       bool
}

// recurrenceLabel turns a schedule's raw Recurrence/Weekdays/IntervalDays
// into the plain-English form the phase brief names ("Weekly · Mon, Wed" /
// "Daily" / "Every 3 days").
func recurrenceLabel(sc convo.Schedule) string {
	switch sc.Recurrence {
	case convo.RecurrenceDaily:
		return "Daily"
	case convo.RecurrenceWeekly:
		return "Weekly · " + weekdayShortList(sc.Weekdays)
	case convo.RecurrenceInterval:
		n := sc.IntervalDays
		if n <= 0 {
			n = 1
		}
		if n == 1 {
			return "Every day"
		}
		return fmt.Sprintf("Every %d days", n)
	default:
		return sc.Recurrence
	}
}

// weekdayShortList renders a stored "1,3,5" Weekdays column as "Mon, Wed,
// Fri" — an unparseable/blank entry is skipped, the same tolerance
// scheduleFiresOn (planner.go) applies when it reads this column.
func weekdayShortList(csv string) string {
	var names []string
	for _, p := range strings.Split(csv, ",") {
		if name, ok := weekdayShortNames[strings.TrimSpace(p)]; ok {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "—"
	}
	return strings.Join(names, ", ")
}

// windowLabel renders "09:00–17:00 Asia/Jakarta" — the phase brief's exact
// example shape.
func windowLabel(sc convo.Schedule) string {
	return fmt.Sprintf("%s–%s %s", sc.WindowStart, sc.WindowEnd, sc.Timezone)
}

func toScheduleView(sc convo.Schedule, receiverCount int) scheduleView {
	return scheduleView{
		Schedule: sc, ReceiverCount: receiverCount,
		RecurrenceLabel: recurrenceLabel(sc), WindowLabel: windowLabel(sc),
		NoEndDate: sc.EndDate == "",
	}
}

// selfConvoSchedulesLimit mirrors selfConvoTemplatesLimit's own 200-row
// ceiling (self_convo_ui.go) — neither the list page nor the rows partial
// paginates.
const selfConvoSchedulesLimit = 200

// selfConvoSchedules lists every schedule, newest first, with each one's
// receiver count resolved. h.Convo == nil renders an empty list, the same
// nil-guard selfConvoRuns/selfConvoTemplates (self_convo_ui.go) use.
func (h *handlers) selfConvoSchedules(r *http.Request) []scheduleView {
	if h.Convo == nil {
		return nil
	}
	scheds, err := h.Convo.ListSchedules(r.Context(), selfConvoSchedulesLimit)
	if err != nil {
		return nil
	}
	out := make([]scheduleView, len(scheds))
	for i, sc := range scheds {
		n := 0
		if recs, err := h.Convo.ScheduleReceivers(r.Context(), sc.ID); err == nil {
			n = len(recs)
		}
		out[i] = toScheduleView(sc, n)
	}
	return out
}

// scheduleRowsData feeds the convo_schedule_rows partial — the SAME
// partial the poll and every pause/resume/stop/delete outcome render,
// mirroring convoRunRowsData's/convoTemplateRowsData's own rule.
type scheduleRowsData struct {
	Enabled   bool
	Error     string
	Schedules []scheduleView
}

func (h *handlers) scheduleRowsWithError(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.Renderer.RenderPartial(w, "convo_schedule_rows", scheduleRowsData{
		Enabled: h.SelfConvoEnabled, Error: errMsg, Schedules: h.selfConvoSchedules(r),
	})
}

// GetSelfConvoScheduleRows is the list page's 5s poll target — same
// interval as convo_run_rows' own hx-trigger (self_convo/trigger.html).
func (h *handlers) GetSelfConvoScheduleRows(w http.ResponseWriter, r *http.Request) {
	h.scheduleRowsWithError(w, r, "")
}

// schedulesPageData drives GET /self-convo/schedules.
type schedulesPageData struct {
	PageBase
	Enabled bool
}

// GetSelfConvoSchedules renders the schedules list page. Never gated by
// SelfConvoEnabled: reviewing/pausing/stopping/deleting existing schedules
// must stay possible even after the flag is turned off — only creating one
// (and resuming one to active) sends anything.
func (h *handlers) GetSelfConvoSchedules(w http.ResponseWriter, r *http.Request) {
	data := schedulesPageData{Enabled: h.SelfConvoEnabled}
	data.PageBase = h.newPageBase(r, "Convo Schedules", "/self-convo/schedules")
	h.Renderer.RenderPage(w, "self_convo_schedules", data)
}

// schedulePlannerGuard is Pause/Resume/Stop's shared gate: both the feature
// flag AND a wired Planner are required, since cmd/app/selfconvo.go's
// startSelfConvo returns a nil Planner under the exact same condition the
// flag is off — see Deps.Planner's own doc comment (routes.go).
func (h *handlers) schedulePlannerGuard(w http.ResponseWriter, r *http.Request) bool {
	if !h.SelfConvoEnabled || h.Planner == nil {
		h.scheduleRowsWithError(w, r, scheduleDisabledMessage)
		return false
	}
	return true
}

// scheduleActionErrorMessage maps a Planner action's error to operator
// copy. convo.ErrScheduleFinished (a terminal schedule refusing to move
// again) gets friendly wording; Resume's own "still unhealthy" error
// (planner.go) already carries the ORIGINAL stall reason inline and is
// passed through verbatim — the phase brief's explicit requirement that a
// refused resume renders that reason, not a generic failure.
func scheduleActionErrorMessage(err error) string {
	if errors.Is(err, convo.ErrScheduleFinished) {
		return "schedule is already finished or stopped"
	}
	return err.Error()
}

// PostSelfConvoSchedulePause is POST /self-convo/schedules/{id}/pause.
func (h *handlers) PostSelfConvoSchedulePause(w http.ResponseWriter, r *http.Request) {
	if !h.schedulePlannerGuard(w, r) {
		return
	}
	if err := h.Planner.Pause(r.Context(), r.PathValue("id")); err != nil {
		h.scheduleRowsWithError(w, r, scheduleActionErrorMessage(err))
		return
	}
	h.GetSelfConvoScheduleRows(w, r)
}

// PostSelfConvoScheduleResume is POST /self-convo/schedules/{id}/resume.
func (h *handlers) PostSelfConvoScheduleResume(w http.ResponseWriter, r *http.Request) {
	if !h.schedulePlannerGuard(w, r) {
		return
	}
	if err := h.Planner.Resume(r.Context(), r.PathValue("id")); err != nil {
		h.scheduleRowsWithError(w, r, scheduleActionErrorMessage(err))
		return
	}
	h.GetSelfConvoScheduleRows(w, r)
}

// PostSelfConvoScheduleStop is POST /self-convo/schedules/{id}/stop — the
// irreversible one; convo_schedule_rows.html's hx-confirm on the Stop
// button is where the operator-facing warning lives (a stopped schedule
// cannot be resumed, and a run already scheduled for today still goes out).
func (h *handlers) PostSelfConvoScheduleStop(w http.ResponseWriter, r *http.Request) {
	if !h.schedulePlannerGuard(w, r) {
		return
	}
	if err := h.Planner.Stop_(r.Context(), r.PathValue("id")); err != nil {
		h.scheduleRowsWithError(w, r, scheduleActionErrorMessage(err))
		return
	}
	h.GetSelfConvoScheduleRows(w, r)
}

// PostSelfConvoScheduleDelete is POST /self-convo/schedules/{id}/delete.
// Deliberately NOT gated by SelfConvoEnabled: deleting a schedule can only
// ever reduce future automated sending, the same reasoning template
// deletion is ungated by (self_convo_templates_ui.go's doc comment).
func (h *handlers) PostSelfConvoScheduleDelete(w http.ResponseWriter, r *http.Request) {
	if h.Convo == nil {
		h.scheduleRowsWithError(w, r, "self convo is not available on this deployment")
		return
	}
	if err := h.Convo.DeleteSchedule(r.Context(), r.PathValue("id")); err != nil {
		msg := err.Error()
		if errors.Is(err, convo.ErrNotFound) {
			msg = "schedule not found"
		}
		h.scheduleRowsWithError(w, r, msg)
		return
	}
	h.GetSelfConvoScheduleRows(w, r)
}

// scheduleViewPageData drives GET /self-convo/schedules/{id}.
type scheduleViewPageData struct {
	PageBase
	Enabled   bool
	Schedule  scheduleView
	Receivers []convo.ScheduleReceiver
}

// GetSelfConvoScheduleView renders one schedule's settings, receivers,
// stall reason (if any) and — via the embedded table's own hx-get — its
// recent runs.
func (h *handlers) GetSelfConvoScheduleView(w http.ResponseWriter, r *http.Request) {
	if h.Convo == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	sc, err := h.Convo.GetSchedule(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	receivers, err := h.Convo.ScheduleReceivers(r.Context(), id)
	if err != nil {
		receivers = nil
	}
	data := scheduleViewPageData{
		Enabled: h.SelfConvoEnabled, Schedule: toScheduleView(sc, len(receivers)), Receivers: receivers,
	}
	data.PageBase = h.newPageBase(r, "Schedule: "+sc.Name, "/self-convo/schedules")
	h.Renderer.RenderPage(w, "self_convo_schedule_view", data)
}

// selfConvoScheduleRunsLimit is the schedule detail page's own runs-table
// cap — same 50-row convention as selfConvoRuns (self_convo_ui.go).
const selfConvoScheduleRunsLimit = 50

// GetSelfConvoScheduleRunRows is schedule_view.html's runs table poll
// target. It renders the SAME convo_run_rows partial the trigger page's
// own run table uses (toConvoRunView, self_convo_ui.go) — extending an
// existing generic partial with a schedule-scoped data source rather than
// forking a near-identical copy of it.
func (h *handlers) GetSelfConvoScheduleRunRows(w http.ResponseWriter, r *http.Request) {
	var runs []convoRunView
	if h.Convo != nil {
		if rs, err := h.Convo.RunsForSchedule(r.Context(), r.PathValue("id"), selfConvoScheduleRunsLimit); err == nil {
			runs = make([]convoRunView, len(rs))
			for i, run := range rs {
				runs[i] = toConvoRunView(run)
			}
		}
	}
	h.Renderer.RenderPartial(w, "convo_run_rows", convoRunRowsData{Enabled: h.SelfConvoEnabled, Runs: runs})
}
