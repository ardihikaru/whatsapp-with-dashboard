// group_broadcast_schedule_ui.go: the operator UI for Group Broadcast
// Schedule — turning a persisted groupbroadcast (domain) Schedule into
// pages and HTMX partials. Structurally a close copy of
// self_convo_schedule_ui.go's shape (scheduleForm -> validateScheduleForm
// -> create/update), a NEW SIBLING page rather than a bolt-on: the two
// features' fields barely overlap (topic/template/chain vs.
// group/windows/lateness), and CLAUDE.md's own GroupJID/GroupCode warning
// is exactly the kind of same-word confusion worth avoiding by keeping them
// structurally separate.
package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	gb "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/groupbroadcast"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// groupBroadcastDisabledMessage mirrors scheduleDisabledMessage
// (self_convo_schedule_ui.go) for this feature's own mutations.
const groupBroadcastDisabledMessage = "Group Broadcast Schedule is disabled on this deployment. Set WSM_GROUP_BROADCAST_ENABLED=true and restart to enable it."

// defaultGroupBroadcastTimezone matches migrations/0011's own column
// default and is what a blank/new form starts with.
const defaultGroupBroadcastTimezone = "Asia/Jakarta"

// gbScheduleForm is the raw create/edit form, kept as strings — same
// convention as scheduleForm (self_convo_schedule_ui.go).
type gbScheduleForm struct {
	Name                string
	SenderID            string
	KnownGroupID        string
	GroupJID            string
	GroupName           string
	ReceiverIDs         []string
	Recurrence          string
	Weekdays            []string
	IntervalDays        string
	SenderWindowStart   string
	SenderWindowEnd     string
	ReceiverWindowStart string
	ReceiverWindowEnd   string
	LateCutoff          string
	LangIDWeight        string
	Timezone            string
	StartDate           string
	EndDate             string
}

func newGBScheduleForm(now time.Time) gbScheduleForm {
	return gbScheduleForm{
		Recurrence:          gb.RecurrenceDaily,
		SenderWindowStart:   "07:55",
		SenderWindowEnd:     "08:16",
		ReceiverWindowStart: "08:17",
		ReceiverWindowEnd:   "09:30",
		LateCutoff:          "08:30",
		LangIDWeight:        "70",
		Timezone:            defaultGroupBroadcastTimezone,
		StartDate:           now.Format(dateInputLayout),
	}
}

func gbScheduleFormFromSchedule(sc gb.Schedule, receivers []gb.ScheduleReceiver) gbScheduleForm {
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
	return gbScheduleForm{
		Name: sc.Name, SenderID: sc.SenderSessionID,
		KnownGroupID: sc.KnownGroupID, GroupJID: sc.GroupJID, GroupName: sc.GroupName,
		ReceiverIDs: ids,
		Recurrence:  sc.Recurrence, Weekdays: weekdays, IntervalDays: intervalDays,
		SenderWindowStart: sc.SenderWindowStart, SenderWindowEnd: sc.SenderWindowEnd,
		ReceiverWindowStart: sc.ReceiverWindowStart, ReceiverWindowEnd: sc.ReceiverWindowEnd,
		LateCutoff: sc.LateCutoff, LangIDWeight: strconv.Itoa(sc.LangIDWeight),
		Timezone: sc.Timezone, StartDate: sc.StartDate, EndDate: sc.EndDate,
	}
}

func gbScheduleFormFromRequest(r *http.Request) gbScheduleForm {
	return gbScheduleForm{
		Name:                strings.TrimSpace(r.FormValue("name")),
		SenderID:            strings.TrimSpace(r.FormValue("sender_id")),
		KnownGroupID:        strings.TrimSpace(r.FormValue("known_group_id")),
		GroupJID:            strings.TrimSpace(r.FormValue("group_jid")),
		GroupName:           strings.TrimSpace(r.FormValue("group_name")),
		ReceiverIDs:         dedupeStrings(r.Form["receiver_id"]),
		Recurrence:          strings.TrimSpace(r.FormValue("recurrence")),
		Weekdays:            r.Form["weekday"],
		IntervalDays:        strings.TrimSpace(r.FormValue("interval_days")),
		SenderWindowStart:   strings.TrimSpace(r.FormValue("sender_window_start")),
		SenderWindowEnd:     strings.TrimSpace(r.FormValue("sender_window_end")),
		ReceiverWindowStart: strings.TrimSpace(r.FormValue("receiver_window_start")),
		ReceiverWindowEnd:   strings.TrimSpace(r.FormValue("receiver_window_end")),
		LateCutoff:          strings.TrimSpace(r.FormValue("late_cutoff")),
		LangIDWeight:        strings.TrimSpace(r.FormValue("lang_id_weight")),
		Timezone:            strings.TrimSpace(r.FormValue("timezone")),
		StartDate:           strings.TrimSpace(r.FormValue("start_date")),
		EndDate:             strings.TrimSpace(r.FormValue("end_date")),
	}
}

// gbScheduleFormPageData drives GET/POST .../schedules/new and
// .../{id}/edit.
type gbScheduleFormPageData struct {
	PageBase
	Enabled     bool
	IsEdit      bool
	ScheduleID  string
	Form        gbScheduleForm
	Error       string
	Sessions    []wa.Snapshot
	KnownGroups []knownGroupOption
	Timezones   []string
	Weekdays    []weekdayOption
	ReceiverSet map[string]bool
}

// knownGroupOption is one <option> of the known-group <select> —
// SessionID lets the form's own JS filter it to the chosen sender.
type knownGroupOption struct {
	ID        string
	SessionID string
	Name      string
	GroupJID  string
}

func (h *handlers) gbKnownGroupOptions(r *http.Request) []knownGroupOption {
	groups := h.knownGroups(r)
	out := make([]knownGroupOption, len(groups))
	for i, g := range groups {
		out[i] = knownGroupOption{ID: g.ID, SessionID: g.SessionID, Name: g.Name, GroupJID: g.GroupJID}
	}
	return out
}

func (h *handlers) newGBScheduleFormPageData(r *http.Request, isEdit bool, scheduleID string, form gbScheduleForm, errMsg string) gbScheduleFormPageData {
	data := gbScheduleFormPageData{
		Enabled: h.GroupBroadcastEnabled, IsEdit: isEdit, ScheduleID: scheduleID,
		Form: form, Error: errMsg,
		Sessions: h.scheduleSessions(), KnownGroups: h.gbKnownGroupOptions(r),
		Timezones: scheduleTimezones, Weekdays: weekdayOptions(strings.Join(form.Weekdays, ",")),
		ReceiverSet: receiverSet(form.ReceiverIDs),
	}
	title := "New Group Broadcast Schedule"
	if isEdit {
		title = "Edit Group Broadcast Schedule"
	}
	data.PageBase = h.newPageBase(r, title, "/group-broadcast/schedules")
	return data
}

func (h *handlers) renderGBScheduleForm(w http.ResponseWriter, r *http.Request, isEdit bool, scheduleID string, form gbScheduleForm, errMsg string) {
	data := h.newGBScheduleFormPageData(r, isEdit, scheduleID, form, errMsg)
	h.Renderer.RenderPage(w, "group_broadcast_schedule_form", data)
}

// GetGroupBroadcastScheduleNew renders the blank create form. Never gated
// by GroupBroadcastEnabled — same "the form still renders" rule
// self_convo_schedule_ui.go follows.
func (h *handlers) GetGroupBroadcastScheduleNew(w http.ResponseWriter, r *http.Request) {
	h.renderGBScheduleForm(w, r, false, "", newGBScheduleForm(time.Now().UTC()), "")
}

// GetGroupBroadcastScheduleEdit renders id's current settings as the edit
// form.
func (h *handlers) GetGroupBroadcastScheduleEdit(w http.ResponseWriter, r *http.Request) {
	if h.GroupBroadcast == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	sc, err := h.GroupBroadcast.GetSchedule(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	receivers, err := h.GroupBroadcast.ScheduleReceivers(r.Context(), id)
	if err != nil {
		receivers = nil
	}
	h.renderGBScheduleForm(w, r, true, id, gbScheduleFormFromSchedule(sc, receivers), "")
}

// gbScheduleInputs is validateGBScheduleForm's resolved, typed output.
type gbScheduleInputs struct {
	Name                                   string
	SenderID, SenderName, SenderPhone      string
	KnownGroupID, GroupJID, GroupName      string
	Receivers                              []gb.ScheduleReceiver
	Recurrence                             string
	Weekdays                               string
	IntervalDays                           int
	SenderWindowStart, SenderWindowEnd     string
	ReceiverWindowStart, ReceiverWindowEnd string
	LateCutoff                             string
	LangIDWeight                           int
	Timezone                               string
	StartDate, EndDate                     string
}

// validateGBScheduleForm checks every rule in the order an operator would
// fix them, and returns the FIRST violation's message — same shape as
// validateScheduleForm. Explicit form-validation tests the plan names:
// rejects the sender as its own receiver; rejects an unknown group JID.
func (h *handlers) validateGBScheduleForm(r *http.Request, form gbScheduleForm) (gbScheduleInputs, string) {
	var out gbScheduleInputs

	if form.Name == "" {
		return out, "name is required"
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

	rawJID := form.GroupJID
	groupName := form.GroupName
	if form.KnownGroupID != "" && h.KnownGroups != nil {
		kg, err := h.KnownGroups.Get(r.Context(), form.KnownGroupID)
		if err == nil {
			rawJID = kg.GroupJID
			if groupName == "" {
				groupName = kg.Name
			}
			out.KnownGroupID = kg.ID
		}
	}
	jid, err := wa.ParseGroupJID(rawJID)
	if err != nil {
		return out, "unknown group JID — pick a known group or paste a valid WhatsApp group JID"
	}
	out.GroupJID = jid.String()
	if groupName == "" {
		groupName = out.GroupJID
	}
	out.GroupName = groupName

	receiverIDs := dedupeStrings(form.ReceiverIDs)
	if len(receiverIDs) == 0 {
		return out, "tick at least one receiver"
	}
	for _, rid := range receiverIDs {
		if rid == form.SenderID {
			return out, "the sender cannot also be a receiver"
		}
	}
	receivers := make([]gb.ScheduleReceiver, 0, len(receiverIDs))
	for i, rid := range receiverIDs {
		s, ok := h.Manager.Get(rid)
		if !ok {
			return out, "unknown receiver session"
		}
		receivers = append(receivers, gb.ScheduleReceiver{
			SessionID: s.ID, SessionName: s.Name, Phone: s.Phone, Position: i,
		})
	}
	out.Receivers = receivers

	switch form.Recurrence {
	case gb.RecurrenceDaily, gb.RecurrenceWeekly, gb.RecurrenceInterval:
		out.Recurrence = form.Recurrence
	default:
		return out, "unknown recurrence"
	}
	if out.Recurrence == gb.RecurrenceWeekly {
		days := validWeekdays(form.Weekdays)
		if len(days) == 0 {
			return out, "tick at least one weekday for a weekly schedule"
		}
		out.Weekdays = strings.Join(days, ",")
	}
	if out.Recurrence == gb.RecurrenceInterval {
		n, err := strconv.Atoi(form.IntervalDays)
		if err != nil || n < 1 {
			return out, "interval must be at least 1 day"
		}
		out.IntervalDays = n
	}

	if !validScheduleClock(form.SenderWindowStart) || !validScheduleClock(form.SenderWindowEnd) {
		return out, "sender window must be valid HH:MM times"
	}
	if form.SenderWindowEnd <= form.SenderWindowStart {
		return out, "sender window end must be after its start"
	}
	out.SenderWindowStart, out.SenderWindowEnd = form.SenderWindowStart, form.SenderWindowEnd

	if !validScheduleClock(form.ReceiverWindowStart) || !validScheduleClock(form.ReceiverWindowEnd) {
		return out, "receiver window must be valid HH:MM times"
	}
	if form.ReceiverWindowEnd <= form.ReceiverWindowStart {
		return out, "receiver window end must be after its start"
	}
	out.ReceiverWindowStart, out.ReceiverWindowEnd = form.ReceiverWindowStart, form.ReceiverWindowEnd

	if !validScheduleClock(form.LateCutoff) {
		return out, "late cutoff must be a valid HH:MM time"
	}
	out.LateCutoff = form.LateCutoff

	weight, err := strconv.Atoi(form.LangIDWeight)
	if err != nil || weight < 0 || weight > 100 {
		return out, "Indonesian weight must be a number between 0 and 100"
	}
	out.LangIDWeight = weight

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

// PostGroupBroadcastScheduleCreate is POST /group-broadcast/schedules.
func (h *handlers) PostGroupBroadcastScheduleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderGBScheduleForm(w, r, false, "", gbScheduleForm{}, "invalid form submission")
		return
	}
	form := gbScheduleFormFromRequest(r)
	if h.GroupBroadcast == nil {
		h.renderGBScheduleForm(w, r, false, "", form, "group broadcast is not available on this deployment")
		return
	}
	if !h.GroupBroadcastEnabled {
		h.renderGBScheduleForm(w, r, false, "", form, groupBroadcastDisabledMessage)
		return
	}
	in, errMsg := h.validateGBScheduleForm(r, form)
	if errMsg != "" {
		h.renderGBScheduleForm(w, r, false, "", form, errMsg)
		return
	}

	sc := &gb.Schedule{
		ID: wa.NewID(), Name: in.Name,
		SenderSessionID: in.SenderID, SenderName: in.SenderName, SenderPhone: in.SenderPhone,
		KnownGroupID: in.KnownGroupID, GroupJID: in.GroupJID, GroupName: in.GroupName,
		Recurrence: in.Recurrence, Weekdays: in.Weekdays, IntervalDays: in.IntervalDays,
		SenderWindowStart: in.SenderWindowStart, SenderWindowEnd: in.SenderWindowEnd,
		ReceiverWindowStart: in.ReceiverWindowStart, ReceiverWindowEnd: in.ReceiverWindowEnd,
		LateCutoff: in.LateCutoff, LangIDWeight: in.LangIDWeight,
		Timezone: in.Timezone, StartDate: in.StartDate, EndDate: in.EndDate,
		State: gb.ScheduleActive,
	}
	if err := h.GroupBroadcast.InsertSchedule(r.Context(), sc, in.Receivers); err != nil {
		h.renderGBScheduleForm(w, r, false, "", form, err.Error())
		return
	}
	http.Redirect(w, r, h.BasePath+"/group-broadcast/schedules", http.StatusSeeOther)
}

// PostGroupBroadcastScheduleUpdate is POST /group-broadcast/schedules/{id}
// — the receiver add/remove endpoint (confirmed decision #7): the operator
// always submits the FULL desired receiver set, and UpdateSchedule
// delete+reinserts it in one transaction, so an edit here takes effect on
// the NEXT occurrence the planner plans and never retroactively touches an
// already-planned one. Fields the form does not carry
// (State/PausedReason/PausedAt/LastPlannedDate/CreatedAt) are preserved
// from the existing row — a settings edit must never resurrect a
// paused/stalled schedule to active.
func (h *handlers) PostGroupBroadcastScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.GroupBroadcast == nil {
		http.NotFound(w, r)
		return
	}
	existing, err := h.GroupBroadcast.GetSchedule(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderGBScheduleForm(w, r, true, id, gbScheduleForm{}, "invalid form submission")
		return
	}
	form := gbScheduleFormFromRequest(r)
	if !h.GroupBroadcastEnabled {
		h.renderGBScheduleForm(w, r, true, id, form, groupBroadcastDisabledMessage)
		return
	}
	in, errMsg := h.validateGBScheduleForm(r, form)
	if errMsg != "" {
		h.renderGBScheduleForm(w, r, true, id, form, errMsg)
		return
	}

	existing.Name = in.Name
	existing.SenderSessionID, existing.SenderName, existing.SenderPhone = in.SenderID, in.SenderName, in.SenderPhone
	existing.KnownGroupID, existing.GroupJID, existing.GroupName = in.KnownGroupID, in.GroupJID, in.GroupName
	existing.Recurrence = in.Recurrence
	existing.Weekdays = in.Weekdays
	existing.IntervalDays = in.IntervalDays
	existing.SenderWindowStart, existing.SenderWindowEnd = in.SenderWindowStart, in.SenderWindowEnd
	existing.ReceiverWindowStart, existing.ReceiverWindowEnd = in.ReceiverWindowStart, in.ReceiverWindowEnd
	existing.LateCutoff, existing.LangIDWeight = in.LateCutoff, in.LangIDWeight
	existing.Timezone = in.Timezone
	existing.StartDate, existing.EndDate = in.StartDate, in.EndDate

	if err := h.GroupBroadcast.UpdateSchedule(r.Context(), &existing, in.Receivers); err != nil {
		h.renderGBScheduleForm(w, r, true, id, form, err.Error())
		return
	}
	http.Redirect(w, r, h.BasePath+"/group-broadcast/schedules/"+id, http.StatusSeeOther)
}

// gbScheduleView wraps a gb.Schedule with the presentation fields the rows
// partial/view page need.
type gbScheduleView struct {
	gb.Schedule
	ReceiverCount       int
	RecurrenceLabel     string
	SenderWindowLabel   string
	ReceiverWindowLabel string
	NoEndDate           bool
}

func gbRecurrenceLabel(sc gb.Schedule) string {
	switch sc.Recurrence {
	case gb.RecurrenceDaily:
		return "Daily"
	case gb.RecurrenceWeekly:
		return "Weekly · " + weekdayShortList(sc.Weekdays)
	case gb.RecurrenceInterval:
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

func toGBScheduleView(sc gb.Schedule, receiverCount int) gbScheduleView {
	return gbScheduleView{
		Schedule: sc, ReceiverCount: receiverCount, RecurrenceLabel: gbRecurrenceLabel(sc),
		SenderWindowLabel:   fmt.Sprintf("%s–%s %s", sc.SenderWindowStart, sc.SenderWindowEnd, sc.Timezone),
		ReceiverWindowLabel: fmt.Sprintf("%s–%s %s", sc.ReceiverWindowStart, sc.ReceiverWindowEnd, sc.Timezone),
		NoEndDate:           sc.EndDate == "",
	}
}

// groupBroadcastSchedulesLimit mirrors selfConvoSchedulesLimit's own
// 200-row ceiling.
const groupBroadcastSchedulesLimit = 200

func (h *handlers) groupBroadcastSchedules(r *http.Request) []gbScheduleView {
	if h.GroupBroadcast == nil {
		return nil
	}
	scheds, err := h.GroupBroadcast.ListSchedules(r.Context(), groupBroadcastSchedulesLimit)
	if err != nil {
		return nil
	}
	out := make([]gbScheduleView, len(scheds))
	for i, sc := range scheds {
		n := 0
		if recs, err := h.GroupBroadcast.ScheduleReceivers(r.Context(), sc.ID); err == nil {
			n = len(recs)
		}
		out[i] = toGBScheduleView(sc, n)
	}
	return out
}

// gbScheduleRowsData feeds the group_broadcast_schedule_rows partial.
type gbScheduleRowsData struct {
	Enabled   bool
	Error     string
	Schedules []gbScheduleView
}

func (h *handlers) gbScheduleRowsWithError(w http.ResponseWriter, r *http.Request, errMsg string) {
	h.Renderer.RenderPartial(w, "group_broadcast_schedule_rows", gbScheduleRowsData{
		Enabled: h.GroupBroadcastEnabled, Error: errMsg, Schedules: h.groupBroadcastSchedules(r),
	})
}

// GetGroupBroadcastScheduleRows is the list page's poll target.
func (h *handlers) GetGroupBroadcastScheduleRows(w http.ResponseWriter, r *http.Request) {
	h.gbScheduleRowsWithError(w, r, "")
}

// gbSchedulesPageData drives GET /group-broadcast/schedules.
type gbSchedulesPageData struct {
	PageBase
	Enabled bool
}

// GetGroupBroadcastSchedules renders the schedules list page. Never gated
// by GroupBroadcastEnabled — reviewing/pausing/stopping/deleting existing
// schedules must stay possible even after the flag is off.
func (h *handlers) GetGroupBroadcastSchedules(w http.ResponseWriter, r *http.Request) {
	data := gbSchedulesPageData{Enabled: h.GroupBroadcastEnabled}
	data.PageBase = h.newPageBase(r, "Group Broadcast Schedules", "/group-broadcast/schedules")
	h.Renderer.RenderPage(w, "group_broadcast_schedules", data)
}

// gbPlannerGuard is Pause/Resume/Stop's shared gate — mirrors
// schedulePlannerGuard (self_convo_schedule_ui.go).
func (h *handlers) gbPlannerGuard(w http.ResponseWriter, r *http.Request) bool {
	if !h.GroupBroadcastEnabled || h.GroupBroadcastPlanner == nil {
		h.gbScheduleRowsWithError(w, r, groupBroadcastDisabledMessage)
		return false
	}
	return true
}

func gbScheduleActionErrorMessage(err error) string {
	if errors.Is(err, gb.ErrScheduleFinished) {
		return "schedule is already finished or stopped"
	}
	return err.Error()
}

// PostGroupBroadcastSchedulePause is POST
// /group-broadcast/schedules/{id}/pause.
func (h *handlers) PostGroupBroadcastSchedulePause(w http.ResponseWriter, r *http.Request) {
	if !h.gbPlannerGuard(w, r) {
		return
	}
	if err := h.GroupBroadcastPlanner.Pause(r.Context(), r.PathValue("id")); err != nil {
		h.gbScheduleRowsWithError(w, r, gbScheduleActionErrorMessage(err))
		return
	}
	h.GetGroupBroadcastScheduleRows(w, r)
}

// PostGroupBroadcastScheduleResume is POST
// /group-broadcast/schedules/{id}/resume.
func (h *handlers) PostGroupBroadcastScheduleResume(w http.ResponseWriter, r *http.Request) {
	if !h.gbPlannerGuard(w, r) {
		return
	}
	if err := h.GroupBroadcastPlanner.Resume(r.Context(), r.PathValue("id")); err != nil {
		h.gbScheduleRowsWithError(w, r, gbScheduleActionErrorMessage(err))
		return
	}
	h.GetGroupBroadcastScheduleRows(w, r)
}

// PostGroupBroadcastScheduleStop is POST
// /group-broadcast/schedules/{id}/stop.
func (h *handlers) PostGroupBroadcastScheduleStop(w http.ResponseWriter, r *http.Request) {
	if !h.gbPlannerGuard(w, r) {
		return
	}
	if err := h.GroupBroadcastPlanner.Stop_(r.Context(), r.PathValue("id")); err != nil {
		h.gbScheduleRowsWithError(w, r, gbScheduleActionErrorMessage(err))
		return
	}
	h.GetGroupBroadcastScheduleRows(w, r)
}

// PostGroupBroadcastScheduleDelete is POST
// /group-broadcast/schedules/{id}/delete. Deliberately NOT gated by
// GroupBroadcastEnabled — deleting a schedule can only ever reduce future
// automated sending.
func (h *handlers) PostGroupBroadcastScheduleDelete(w http.ResponseWriter, r *http.Request) {
	if h.GroupBroadcast == nil {
		h.gbScheduleRowsWithError(w, r, "group broadcast is not available on this deployment")
		return
	}
	if err := h.GroupBroadcast.DeleteSchedule(r.Context(), r.PathValue("id")); err != nil {
		msg := err.Error()
		if errors.Is(err, gb.ErrNotFound) {
			msg = "schedule not found"
		}
		h.gbScheduleRowsWithError(w, r, msg)
		return
	}
	h.GetGroupBroadcastScheduleRows(w, r)
}

// gbScheduleViewPageData drives GET /group-broadcast/schedules/{id}.
type gbScheduleViewPageData struct {
	PageBase
	Enabled   bool
	Schedule  gbScheduleView
	Receivers []gb.ScheduleReceiver
}

// GetGroupBroadcastScheduleView renders one schedule's settings, receivers,
// stall reason (if any) and — via the embedded table's own hx-get — its
// recent occurrences.
func (h *handlers) GetGroupBroadcastScheduleView(w http.ResponseWriter, r *http.Request) {
	if h.GroupBroadcast == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	sc, err := h.GroupBroadcast.GetSchedule(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	receivers, err := h.GroupBroadcast.ScheduleReceivers(r.Context(), id)
	if err != nil {
		receivers = nil
	}
	data := gbScheduleViewPageData{
		Enabled: h.GroupBroadcastEnabled, Schedule: toGBScheduleView(sc, len(receivers)), Receivers: receivers,
	}
	data.PageBase = h.newPageBase(r, "Group Broadcast: "+sc.Name, "/group-broadcast/schedules")
	h.Renderer.RenderPage(w, "group_broadcast_schedule_view", data)
}

// gbOccurrenceView wraps a gb.Occurrence with its turns for the detail
// page's history table.
type gbOccurrenceView struct {
	gb.Occurrence
	Turns []gb.Turn
}

// groupBroadcastOccurrencesLimit mirrors selfConvoScheduleRunsLimit's own
// 50-row cap.
const groupBroadcastOccurrencesLimit = 50

// gbOccurrenceRowsData feeds the group_broadcast_occurrence_rows partial.
type gbOccurrenceRowsData struct {
	Enabled     bool
	Occurrences []gbOccurrenceView
}

// GetGroupBroadcastScheduleOccurrenceRows is schedule_view.html's history
// table poll target — role, language, is_late (+reason), source (ai/
// fallback) and state, the operator-visibility this feature needs to be
// trustworthy (see the phase plan's Templates section).
func (h *handlers) GetGroupBroadcastScheduleOccurrenceRows(w http.ResponseWriter, r *http.Request) {
	var occs []gbOccurrenceView
	if h.GroupBroadcast != nil {
		rows, err := h.GroupBroadcast.OccurrencesForSchedule(r.Context(), r.PathValue("id"), groupBroadcastOccurrencesLimit)
		if err == nil {
			occs = make([]gbOccurrenceView, len(rows))
			for i, occ := range rows {
				turns, _ := h.GroupBroadcast.TurnsForOccurrence(r.Context(), occ.ID)
				occs[i] = gbOccurrenceView{Occurrence: occ, Turns: turns}
			}
		}
	}
	h.Renderer.RenderPartial(w, "group_broadcast_occurrence_rows", gbOccurrenceRowsData{
		Enabled: h.GroupBroadcastEnabled, Occurrences: occs,
	})
}
