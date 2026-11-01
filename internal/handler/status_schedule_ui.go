// status_schedule_ui.go: the operator UI for scheduled WhatsApp status posts
// (/status/schedules). UI-only on purpose: there is no /api/v1 route, so no
// wsm-mcp tool is owed (internal/mcp/registry/sync_test.go only watches
// routes_api.go and waxum.Client). The planner/runner is
// internal/statussched; this file only edits wa_status_schedules rows.
//
// House shape follows self_convo_schedule_ui.go: the list is an HTMX rows
// partial, pause/resume/delete swap one <tr>, and the multi-field form is a
// plain full-page POST (multipart, because image/video carry a file) that
// re-renders itself with Form+Error on a rejection.
//
// Recipients: the form lists EVERY contact, all checked by default. If all
// are still checked on submit the schedule is stored as mode=all and resolved
// at FIRE time (contacts added later are included); anything else is stored
// as an explicit custom list.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/schedclock"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/storage"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

const (
	statusScheduleListLimit = 200
	statusTextMaxChars      = 700
	statusCaptionMaxChars   = 1024
	statusScheduleNameMax   = 120
	statusAITopicMaxChars   = 500
	statusNextTextShowChars = 80
	statusScheduleBasePath  = "/status/schedules"
	noObjectsMessage        = "Image and video schedules need object storage, which is not configured on this deployment. Use a text status instead."
)

// statusWeekdayOrder is the chip order on the form: Sunday first, as the
// operator thinks of a week. Values are ISO weekdays (Monday=1 .. Sunday=7)
// so what is stored is what schedclock.FiresOn reads.
var statusWeekdayOrder = []struct{ Value, Label string }{
	{"7", "Sun"}, {"1", "Mon"}, {"2", "Tue"}, {"3", "Wed"}, {"4", "Thu"}, {"5", "Fri"}, {"6", "Sat"},
}

func statusWeekdayOptions(selected []string) []weekdayOption {
	set := map[string]bool{}
	for _, s := range selected {
		set[strings.TrimSpace(s)] = true
	}
	out := make([]weekdayOption, len(statusWeekdayOrder))
	for i, w := range statusWeekdayOrder {
		out[i] = weekdayOption{Value: w.Value, Label: w.Label, Checked: set[w.Value]}
	}
	return out
}

// statusScheduleSummary is the one-line plain-English description of the
// plan. status-schedule.js mirrors this string live as the operator types;
// this server copy is the no-JS fallback and the initial paint.
func statusScheduleSummary(weekdays []string, start, end, tz string, aiWritten bool) string {
	days := weekdayShortList(strings.Join(validWeekdays(weekdays), ","))
	if days == "—" {
		return "Pick at least one weekday."
	}
	out := fmt.Sprintf("%s · once each day, at a random time between %s and %s %s", days, start, end, tz)
	if aiWritten {
		out += " · AI-written"
	}
	return out
}

// statusScheduleForm is the raw form, kept as strings so a rejected
// submission re-renders exactly as typed.
type statusScheduleForm struct {
	Name, SessionID, Kind string
	Text, BgARGB, Caption string
	Weekdays              []string
	WindowStart           string
	WindowEnd             string
	Timezone              string
	StartDate, EndDate    string
	Recipients            []string // JIDs ticked
	// AllRecipients pre-ticks every contact (a blank form, or a stored
	// mode=all schedule); a re-render after a rejection uses Recipients.
	AllRecipients bool
	// Existing media on an edit (shown as "current file", kept when no new
	// file is uploaded and the kind is unchanged).
	MediaFilename string
	// AIEnabled / AITopic: "Write each post with AI" and what to write about.
	AIEnabled bool
	AITopic   string
}

func newStatusScheduleForm(now time.Time) statusScheduleForm {
	return statusScheduleForm{
		Kind: sched.KindText, BgARGB: strconv.FormatUint(uint64(statusColorSwatches[0].ARGB), 10),
		Weekdays: []string{"1", "2", "3", "4", "5"}, WindowStart: "07:00", WindowEnd: "10:00",
		Timezone: defaultScheduleTimezone, StartDate: now.Format(dateInputLayout), AllRecipients: true,
	}
}

func statusScheduleFormFromSchedule(sc sched.Schedule) statusScheduleForm {
	f := statusScheduleForm{
		Name: sc.Name, SessionID: sc.SessionID, Kind: sc.Kind, Text: sc.Text,
		BgARGB: strconv.FormatUint(uint64(sc.BgARGB), 10), Caption: sc.Caption,
		Weekdays: strings.Split(sc.Weekdays, ","), WindowStart: sc.WindowStart, WindowEnd: sc.WindowEnd,
		Timezone: sc.Timezone, StartDate: sc.StartDate, EndDate: sc.EndDate, MediaFilename: sc.MediaFilename,
		AIEnabled: sc.AIEnabled, AITopic: sc.AITopic,
	}
	if sc.RecipientsMode == sched.ModeAll {
		f.AllRecipients = true
	} else {
		_ = json.Unmarshal([]byte(sc.RecipientsJSON), &f.Recipients)
	}
	return f
}

func statusScheduleFormFromRequest(r *http.Request) statusScheduleForm {
	f := statusScheduleForm{
		Name: strings.TrimSpace(r.FormValue("name")), SessionID: strings.TrimSpace(r.FormValue("session_id")),
		Kind: strings.TrimSpace(r.FormValue("kind")), Text: strings.TrimSpace(r.FormValue("text")),
		BgARGB: strings.TrimSpace(r.FormValue("background_argb")), Caption: strings.TrimSpace(r.FormValue("caption")),
		Weekdays: r.Form["weekday"], WindowStart: strings.TrimSpace(r.FormValue("window_start")),
		WindowEnd: strings.TrimSpace(r.FormValue("window_end")), Timezone: strings.TrimSpace(r.FormValue("timezone")),
		StartDate: strings.TrimSpace(r.FormValue("start_date")), EndDate: strings.TrimSpace(r.FormValue("end_date")),
		Recipients: dedupeStrings(r.Form["recipients"]),
		AIEnabled:  r.FormValue("ai_enabled") != "",
		AITopic:    strings.TrimSpace(r.FormValue("ai_topic")),
	}
	return f
}

// statusScheduleRecipient is one checklist row with its tick state.
type statusScheduleRecipient struct {
	statusRecipientOption
	Checked bool
}

type statusScheduleSessionOption struct {
	ID, Label string
	Connected bool
}

type statusScheduleFormPageData struct {
	PageBase
	IsEdit           bool
	ScheduleID       string
	Form             statusScheduleForm
	Error            string
	Sessions         []statusScheduleSessionOption
	Swatches         []statusColorSwatch
	ActiveSwatch     int
	Timezones        []string
	Weekdays         []weekdayOption
	Recipients       []statusScheduleRecipient
	Summary          string
	ObjectsAvailable bool
	MediaLimits      []mediaKindDisplay
	// AIAvailable: the AI client is wired. false disables the toggle.
	AIAvailable bool
	AITopicMax  int
	AITopicLen  int // runes already typed, for the server-rendered counter
}

func (h *handlers) statusScheduleFormData(r *http.Request, isEdit bool, id string, form statusScheduleForm, errMsg string) statusScheduleFormPageData {
	opts := h.statusRecipientOptions(r)
	set := make(map[string]bool, len(form.Recipients))
	for _, j := range form.Recipients {
		set[j] = true
	}
	recips := make([]statusScheduleRecipient, len(opts))
	for i, o := range opts {
		recips[i] = statusScheduleRecipient{statusRecipientOption: o, Checked: form.AllRecipients || set[o.JID]}
	}
	var sessions []statusScheduleSessionOption
	for _, s := range h.Manager.Snapshots() {
		label := fmt.Sprintf("%s (+%s)", s.Name, s.Phone)
		if !s.Connected {
			label += " — not connected"
		}
		sessions = append(sessions, statusScheduleSessionOption{ID: s.ID, Label: label, Connected: s.Connected})
	}
	active := 0
	for i, sw := range statusColorSwatches {
		if strconv.FormatUint(uint64(sw.ARGB), 10) == form.BgARGB {
			active = i
		}
	}
	tzs := append([]string(nil), scheduleTimezones...)
	if form.Timezone != "" {
		found := false
		for _, z := range tzs {
			found = found || z == form.Timezone
		}
		if !found {
			tzs = append(tzs, form.Timezone)
		}
	}
	title := "New Status Schedule"
	if isEdit {
		title = "Edit Status Schedule"
	}
	data := statusScheduleFormPageData{
		IsEdit: isEdit, ScheduleID: id, Form: form, Error: errMsg, Sessions: sessions,
		Swatches: statusColorSwatches, ActiveSwatch: active, Timezones: tzs,
		Weekdays: statusWeekdayOptions(form.Weekdays), Recipients: recips,
		Summary:          statusScheduleSummary(form.Weekdays, form.WindowStart, form.WindowEnd, form.Timezone, form.AIEnabled),
		ObjectsAvailable: h.Objects != nil, MediaLimits: h.mediaKindDisplays(),
		AIAvailable: h.StatusAIAvailable, AITopicMax: statusAITopicMaxChars, AITopicLen: len([]rune(form.AITopic)),
	}
	data.PageBase = h.newPageBase(r, title, statusScheduleBasePath)
	return data
}

func (h *handlers) renderStatusScheduleForm(w http.ResponseWriter, r *http.Request, isEdit bool, id string, form statusScheduleForm, errMsg string) {
	h.Renderer.RenderPage(w, "status_schedule_form", h.statusScheduleFormData(r, isEdit, id, form, errMsg))
}

// GetStatusScheduleNew renders the blank form: every contact ticked.
func (h *handlers) GetStatusScheduleNew(w http.ResponseWriter, r *http.Request) {
	h.renderStatusScheduleForm(w, r, false, "", newStatusScheduleForm(time.Now().In(h.loc())), "")
}

// GetStatusScheduleEdit renders id's stored settings.
func (h *handlers) GetStatusScheduleEdit(w http.ResponseWriter, r *http.Request) {
	if h.StatusSchedules == nil {
		http.NotFound(w, r)
		return
	}
	sc, err := h.StatusSchedules.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.renderStatusScheduleForm(w, r, true, sc.ID, statusScheduleFormFromSchedule(sc), "")
}

// statusScheduleInputs is the validated form, typed.
type statusScheduleInputs struct {
	sched.Schedule
	newMedia *uploadedStatusMedia // non-nil when this submission carries a file
}

type uploadedStatusMedia struct {
	data        []byte
	contentType string
	filename    string
}

// readStatusScheduleMedia pulls the file for kind out of the multipart form
// (field "file_image" / "file_video"), sniffing the BYTES, not the header.
// (nil, "") means no file was sent.
func (h *handlers) readStatusScheduleMedia(r *http.Request, kind wa.MediaKind) (*uploadedStatusMedia, string) {
	field := "file_" + string(kind)
	file, header, err := r.FormFile(field)
	if err != nil {
		return nil, ""
	}
	defer file.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(file); err != nil {
		return nil, "could not read the uploaded file"
	}
	data := buf.Bytes()
	if size, limit := int64(len(data)), h.mediaKindLimit(kind); size > limit {
		return nil, h.mediaOversizeMessage(kind, size, limit)
	}
	ct, ok := sniffAndValidate(kind, data)
	if !ok {
		return nil, "uploaded file does not look like a " + string(kind) + " (sniffed " + ct + ")"
	}
	return &uploadedStatusMedia{data: data, contentType: ct, filename: sanitizeFilename(header.Filename)}, ""
}

// validateStatusSchedule checks every rule in the order an operator would
// fix them and returns the FIRST violation. existing is the stored row on an
// edit (zero value on create).
func (h *handlers) validateStatusSchedule(r *http.Request, form statusScheduleForm, existing sched.Schedule) (statusScheduleInputs, string) {
	var out statusScheduleInputs
	if form.Name == "" {
		return out, "name is required"
	}
	if len([]rune(form.Name)) > statusScheduleNameMax {
		return out, fmt.Sprintf("name is over %d characters", statusScheduleNameMax)
	}
	if form.SessionID == "" {
		return out, "choose a session"
	}
	if _, ok := h.Manager.Get(form.SessionID); !ok {
		return out, "unknown session"
	}
	out.Name, out.SessionID = form.Name, form.SessionID

	if form.AIEnabled {
		if !h.StatusAIAvailable {
			return out, "AI writing is not configured on this deployment — turn off “Write each post with AI” or ask the operator to set WSM_CODEX_CLI_*"
		}
		if form.AITopic == "" {
			return out, "tell the AI what to write about: topic is required"
		}
		if len([]rune(form.AITopic)) > statusAITopicMaxChars {
			return out, fmt.Sprintf("topic is over %d characters", statusAITopicMaxChars)
		}
		out.AIEnabled, out.AITopic = true, form.AITopic
	}

	switch form.Kind {
	case sched.KindText:
		if form.Text == "" && !form.AIEnabled {
			return out, "status text is required"
		}
		if len([]rune(form.Text)) > statusTextMaxChars {
			return out, fmt.Sprintf("status text is over %d characters", statusTextMaxChars)
		}
		out.Text = form.Text
		if raw := form.BgARGB; raw != "" {
			if n, err := strconv.ParseUint(raw, 10, 32); err == nil {
				out.BgARGB = uint32(n)
			}
		}
	case sched.KindImage, sched.KindVideo:
		if h.Objects == nil {
			return out, noObjectsMessage
		}
		if len([]rune(form.Caption)) > statusCaptionMaxChars {
			return out, fmt.Sprintf("caption is over %d characters", statusCaptionMaxChars)
		}
		out.Caption = form.Caption
		kind := wa.MediaImage
		if form.Kind == sched.KindVideo {
			kind = wa.MediaVideo
		}
		media, msg := h.readStatusScheduleMedia(r, kind)
		if msg != "" {
			return out, msg
		}
		switch {
		case media != nil:
			out.newMedia = media
			out.MediaContentType, out.MediaFilename = media.contentType, media.filename
		case existing.Kind == form.Kind && existing.MediaObjectKey != "":
			out.MediaObjectKey, out.MediaContentType, out.MediaFilename = existing.MediaObjectKey, existing.MediaContentType, existing.MediaFilename
		default:
			return out, "choose a " + string(kind) + " file to post"
		}
	default:
		return out, "choose a status type: text, image or video"
	}
	out.Kind = form.Kind

	days := validWeekdays(form.Weekdays)
	if len(days) == 0 {
		return out, "tick at least one weekday"
	}
	out.Weekdays = strings.Join(days, ",")

	if err := schedclock.ValidateWindow(form.WindowStart, form.WindowEnd); err != nil {
		return out, err.Error()
	}
	out.WindowStart, out.WindowEnd = form.WindowStart, form.WindowEnd

	loc, err := time.LoadLocation(form.Timezone)
	if err != nil {
		return out, fmt.Sprintf("unknown timezone %q", form.Timezone)
	}
	out.Timezone = form.Timezone

	start := form.StartDate
	if start == "" {
		start = time.Now().In(loc).Format(dateInputLayout)
	}
	if _, err := time.Parse(dateInputLayout, start); err != nil {
		return out, "start date must be a valid date"
	}
	out.StartDate = start
	if form.EndDate != "" {
		if _, err := time.Parse(dateInputLayout, form.EndDate); err != nil {
			return out, "end date must be a valid date"
		}
		if form.EndDate < start {
			return out, "end date must not be before start date"
		}
	}
	out.EndDate = form.EndDate

	opts := h.statusRecipientOptions(r)
	if len(opts) == 0 {
		return out, "there are no contacts yet — add one on the Contacts page first"
	}
	ticked := make(map[string]bool, len(form.Recipients))
	for _, j := range form.Recipients {
		ticked[j] = true
	}
	var chosen []string
	for _, o := range opts {
		if ticked[o.JID] {
			chosen = append(chosen, o.JID)
		}
	}
	if len(chosen) == 0 {
		return out, "tick at least one recipient"
	}
	if len(chosen) == len(opts) {
		out.RecipientsMode, out.RecipientsJSON = sched.ModeAll, ""
	} else {
		if _, msg := parseStatusRecipients(chosen); msg != "" {
			return out, msg
		}
		b, _ := json.Marshal(chosen)
		out.RecipientsMode, out.RecipientsJSON = sched.ModeCustom, string(b)
	}
	return out, ""
}

// storeStatusScheduleMedia writes the uploaded file to object storage — a
// HARD requirement here (unlike the best-effort media archive): the file is
// re-fetched at every fire, so a schedule whose file never landed would fail
// forever. Returns the object key.
func (h *handlers) storeStatusScheduleMedia(ctx context.Context, scheduleID, sessionID string, m *uploadedStatusMedia) (string, error) {
	key := storage.ObjectKey(sessionID, "statusschedule-"+scheduleID+"-"+wa.NewID(), time.Now().UTC(), filepath.Ext(m.filename))
	if err := h.Objects.Put(ctx, key, bytes.NewReader(m.data), int64(len(m.data)), m.contentType); err != nil {
		return "", fmt.Errorf("could not store the media file: %w", err)
	}
	return key, nil
}

func (h *handlers) parseStatusScheduleForm(w http.ResponseWriter, r *http.Request) error {
	limit := h.MediaMaxBytes + mediaUploadSlack
	if h.MediaMaxBytes <= 0 {
		limit = 32 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	err := r.ParseMultipartForm(mediaFormMaxMemory)
	if errors.Is(err, http.ErrNotMultipart) {
		err = nil
	}
	return err
}

// PostStatusScheduleCreate is POST /status/schedules.
func (h *handlers) PostStatusScheduleCreate(w http.ResponseWriter, r *http.Request) {
	if err := h.parseStatusScheduleForm(w, r); err != nil {
		msg := "invalid form submission"
		if isMaxBytesError(err) {
			msg = "upload exceeds the configured size limit"
		}
		h.renderStatusScheduleForm(w, r, false, "", newStatusScheduleForm(time.Now().In(h.loc())), msg)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	form := statusScheduleFormFromRequest(r)
	if h.StatusSchedules == nil {
		h.renderStatusScheduleForm(w, r, false, "", form, "scheduled status posts are not available on this deployment")
		return
	}
	in, errMsg := h.validateStatusSchedule(r, form, sched.Schedule{})
	if errMsg != "" {
		h.renderStatusScheduleForm(w, r, false, "", form, errMsg)
		return
	}
	sc := in.Schedule
	sc.ID, sc.State = wa.NewID(), sched.StateActive
	if in.newMedia != nil {
		key, err := h.storeStatusScheduleMedia(r.Context(), sc.ID, sc.SessionID, in.newMedia)
		if err != nil {
			h.renderStatusScheduleForm(w, r, false, "", form, err.Error())
			return
		}
		sc.MediaObjectKey = key
	}
	if err := h.StatusSchedules.Insert(r.Context(), &sc); err != nil {
		h.deleteStatusScheduleObject(r.Context(), sc.MediaObjectKey)
		h.renderStatusScheduleForm(w, r, false, "", form, err.Error())
		return
	}
	http.Redirect(w, r, h.BasePath+statusScheduleBasePath, http.StatusSeeOther)
}

// PostStatusScheduleUpdate is POST /status/schedules/{id}. State and the
// fire history are preserved (the repo's Update only resets planning).
func (h *handlers) PostStatusScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	if h.StatusSchedules == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	existing, err := h.StatusSchedules.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := h.parseStatusScheduleForm(w, r); err != nil {
		h.renderStatusScheduleForm(w, r, true, id, statusScheduleFormFromSchedule(existing), "invalid form submission")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	form := statusScheduleFormFromRequest(r)
	form.MediaFilename = existing.MediaFilename
	in, errMsg := h.validateStatusSchedule(r, form, existing)
	if errMsg != "" {
		h.renderStatusScheduleForm(w, r, true, id, form, errMsg)
		return
	}
	sc := in.Schedule
	sc.ID = id
	if in.newMedia != nil {
		key, err := h.storeStatusScheduleMedia(r.Context(), id, sc.SessionID, in.newMedia)
		if err != nil {
			h.renderStatusScheduleForm(w, r, true, id, form, err.Error())
			return
		}
		sc.MediaObjectKey = key
	}
	if err := h.StatusSchedules.Update(r.Context(), &sc); err != nil {
		if in.newMedia != nil {
			h.deleteStatusScheduleObject(r.Context(), sc.MediaObjectKey)
		}
		h.renderStatusScheduleForm(w, r, true, id, form, err.Error())
		return
	}
	if existing.MediaObjectKey != "" && existing.MediaObjectKey != sc.MediaObjectKey {
		h.deleteStatusScheduleObject(r.Context(), existing.MediaObjectKey)
	}
	http.Redirect(w, r, h.BasePath+statusScheduleBasePath, http.StatusSeeOther)
}

// deleteStatusScheduleObject removes a stored media object. Best-effort: an
// orphaned object costs storage, never correctness, so failure is only logged.
func (h *handlers) deleteStatusScheduleObject(ctx context.Context, key string) {
	if key == "" || h.Objects == nil {
		return
	}
	if err := h.Objects.Delete(ctx, key); err != nil {
		log.Printf("status schedule: could not delete stored media %s (orphaned, harmless): %v", key, err)
	}
}

// statusScheduleView is a Schedule plus the presentation strings the list
// needs, all pre-formatted in Go (the renderer's convention).
type statusScheduleView struct {
	sched.Schedule
	SessionName     string
	KindLabel       string
	DaysLabel       string
	WindowLabel     string
	NextLabel       string
	LastLabel       string
	RecipientsLabel string
	Editable        bool
	// NextTextLabel is the planned wording for the pending slot, truncated;
	// NextSourceLabel says where it came from ("AI" or "static"). Both are
	// empty unless a post is actually pending.
	NextTextLabel   string
	NextSourceLabel string
	AIWritten       bool
}

func (h *handlers) toStatusScheduleView(sc sched.Schedule) statusScheduleView {
	v := statusScheduleView{
		Schedule: sc, SessionName: "(deleted session)", KindLabel: strings.ToUpper(sc.Kind[:1]) + sc.Kind[1:],
		DaysLabel: weekdayShortList(sc.Weekdays), WindowLabel: fmt.Sprintf("%s–%s %s", sc.WindowStart, sc.WindowEnd, sc.Timezone),
		NextLabel: "—", LastLabel: "never", RecipientsLabel: "All contacts", Editable: true,
	}
	if s, ok := h.Manager.Get(sc.SessionID); ok {
		v.SessionName = s.Name()
	}
	loc, err := time.LoadLocation(sc.Timezone)
	if err != nil {
		loc = time.UTC
	}
	if sc.State == sched.StateActive && !sc.NextFireAt.IsZero() {
		v.NextLabel = sc.NextFireAt.In(loc).Format("Mon 2 Jan 15:04 MST")
		if sc.NextText != "" {
			v.NextTextLabel = truncateRunes(sc.NextText, statusNextTextShowChars)
			v.NextSourceLabel = "static"
			if sc.NextTextSource == sched.TextSourceAI {
				v.NextSourceLabel = "AI"
			}
		}
	}
	v.AIWritten = sc.AIEnabled
	if !sc.LastFiredAt.IsZero() {
		v.LastLabel = sc.LastResult + " · " + sc.LastFiredAt.In(loc).Format("Mon 2 Jan 15:04 MST")
	}
	if sc.RecipientsMode == sched.ModeCustom {
		var js []string
		_ = json.Unmarshal([]byte(sc.RecipientsJSON), &js)
		v.RecipientsLabel = fmt.Sprintf("%d selected", len(js))
	}
	return v
}

func (h *handlers) statusScheduleViews(r *http.Request) []statusScheduleView {
	if h.StatusSchedules == nil {
		return nil
	}
	list, err := h.StatusSchedules.List(r.Context(), statusScheduleListLimit)
	if err != nil {
		return nil
	}
	out := make([]statusScheduleView, len(list))
	for i, sc := range list {
		out[i] = h.toStatusScheduleView(sc)
	}
	return out
}

type statusScheduleRowsData struct {
	Error     string
	Schedules []statusScheduleView
}

// statusScheduleRowData feeds the single-row partial pause/resume return.
type statusScheduleRowData struct {
	Error    string
	Schedule *statusScheduleView
}

type statusSchedulesPageData struct{ PageBase }

// GetStatusSchedules renders the list page; its tbody fills over HTMX.
func (h *handlers) GetStatusSchedules(w http.ResponseWriter, r *http.Request) {
	data := statusSchedulesPageData{}
	data.PageBase = h.newPageBase(r, "Status Schedules", statusScheduleBasePath)
	h.Renderer.RenderPage(w, "status_schedules", data)
}

// GetStatusScheduleRows is the tbody's load target.
func (h *handlers) GetStatusScheduleRows(w http.ResponseWriter, r *http.Request) {
	h.Renderer.RenderPartial(w, "status_schedule_rows", statusScheduleRowsData{Schedules: h.statusScheduleViews(r)})
}

func (h *handlers) statusScheduleRow(w http.ResponseWriter, sc *sched.Schedule, errMsg string) {
	data := statusScheduleRowData{Error: errMsg}
	if sc != nil {
		v := h.toStatusScheduleView(*sc)
		data.Schedule = &v
	}
	h.Renderer.RenderPartial(w, "status_schedule_row_result", data)
}

// statusScheduleToggle is pause/resume's shared body: htmx only swaps 2xx,
// so every outcome renders the row partial at 200 (error alert included).
func (h *handlers) statusScheduleToggle(w http.ResponseWriter, r *http.Request, from []string, to, reason, verb string) {
	if h.StatusSchedules == nil {
		h.statusScheduleRow(w, nil, "scheduled status posts are not available on this deployment")
		return
	}
	id := r.PathValue("id")
	sc, err := h.StatusSchedules.Get(r.Context(), id)
	if err != nil {
		h.statusScheduleRow(w, nil, "schedule not found")
		return
	}
	ok := false
	for _, s := range from {
		ok = ok || sc.State == s
	}
	if !ok {
		h.statusScheduleRow(w, &sc, "cannot "+verb+" a "+sc.State+" schedule")
		return
	}
	if err := h.StatusSchedules.SetState(r.Context(), id, to, reason); err != nil {
		h.statusScheduleRow(w, &sc, err.Error())
		return
	}
	sc, _ = h.StatusSchedules.Get(r.Context(), id)
	h.statusScheduleRow(w, &sc, "")
}

// PostStatusSchedulePause is POST /status/schedules/{id}/pause.
func (h *handlers) PostStatusSchedulePause(w http.ResponseWriter, r *http.Request) {
	h.statusScheduleToggle(w, r, []string{sched.StateActive}, sched.StatePaused, "paused by operator", "pause")
}

// PostStatusScheduleResume is POST /status/schedules/{id}/resume.
func (h *handlers) PostStatusScheduleResume(w http.ResponseWriter, r *http.Request) {
	h.statusScheduleToggle(w, r, []string{sched.StatePaused, sched.StateStalled}, sched.StateActive, "", "resume")
}

// PostStatusScheduleDelete is POST /status/schedules/{id}/delete. Success
// answers an EMPTY 200 so htmx swaps the row away; the stored media object
// goes with it.
func (h *handlers) PostStatusScheduleDelete(w http.ResponseWriter, r *http.Request) {
	if h.StatusSchedules == nil {
		h.statusScheduleRow(w, nil, "scheduled status posts are not available on this deployment")
		return
	}
	id := r.PathValue("id")
	sc, err := h.StatusSchedules.Get(r.Context(), id)
	if err != nil {
		h.statusScheduleRow(w, nil, "schedule not found")
		return
	}
	if err := h.StatusSchedules.Delete(r.Context(), id); err != nil {
		h.statusScheduleRow(w, &sc, err.Error())
		return
	}
	h.deleteStatusScheduleObject(r.Context(), sc.MediaObjectKey)
	w.WriteHeader(http.StatusOK)
}

// truncateRunes shortens s to at most n runes, marking the cut with an
// ellipsis (counted inside n).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
