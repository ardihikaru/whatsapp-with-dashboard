package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
)

func authedPostForm(t *testing.T, srv http.Handler, sessions *auth.Store, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	sess, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: sess.Token})
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	return res
}

func createForm(name, loginID string, extra url.Values) url.Values {
	form := url.Values{"name": {name}, "login_id": {loginID}, "description": {""}}
	for k, v := range extra {
		form[k] = v
	}
	return form
}

// The multi-year presets have to be offered, not just accepted.
func TestExpiryDropdownOffersMultiYearPresets(t *testing.T) {
	srv, sessions, _, _ := newAPIClientsTestServer(t)
	body := authedGet(t, srv, sessions, "/admin/api-clients").Body.String()
	for _, want := range []string{
		`value="31536000"`, `>1 tahun<`,
		`value="94608000"`, `>3 tahun<`,
		`value="157680000"`, `>5 tahun<`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expiry dropdown missing %s", want)
		}
	}
}

// Each custom mode has to reach the store as the duration it names.
func TestCustomDurationModes(t *testing.T) {
	cases := []struct {
		name  string
		login string
		extra url.Values
		label string
	}{
		{"years", "tahunan", url.Values{
			"expiry_choice": {"custom"}, "custom_mode": {"years"}, "custom_years": {"7"},
		}, "7 tahun"},
		{"months", "bulanan", url.Values{
			"expiry_choice": {"custom"}, "custom_mode": {"months"}, "custom_months": {"18"},
		}, "18 bulan"},
		{"seconds", "detikan", url.Values{
			"expiry_choice": {"custom"}, "custom_mode": {"seconds"}, "custom_ttl_seconds": {"3600"},
		}, "1h0m0s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, sessions, _, _ := newAPIClientsTestServer(t)
			res := authedPostForm(t, srv, sessions, "/admin/api-clients",
				createForm("Klien "+tc.name, tc.login, tc.extra))
			if res.Code != http.StatusOK {
				t.Fatalf("create = %d: %s", res.Code, res.Body.String())
			}
			if !strings.Contains(res.Body.String(), tc.label) {
				t.Fatalf("table does not show %q: %s", tc.label, res.Body.String())
			}
		})
	}
}

// A date-based client stores an absolute cutoff, so the table names the date
// rather than a duration, and re-opening the client puts the picker back on it.
func TestCustomDateModeRoundTripsThroughTheForm(t *testing.T) {
	srv, sessions, _, _ := newAPIClientsTestServer(t)
	date := time.Now().Local().AddDate(1, 0, 0).Format("2006-01-02")
	res := authedPostForm(t, srv, sessions, "/admin/api-clients",
		createForm("Sampai Tanggal", "sampai", url.Values{
			"expiry_choice": {"custom"}, "custom_mode": {"date"}, "custom_date": {date},
		}))
	if res.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Sampai "+date) {
		t.Fatalf("table does not show the cutoff date: %s", res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `value="`+date+`"`) {
		// The create form resets, so this proves the edit prefill instead.
		editURL := extractEditURL(t, res.Body.String())
		edit := authedGet(t, srv, sessions, editURL).Body.String()
		if !strings.Contains(edit, `value="`+date+`"`) {
			t.Fatalf("edit form did not prefill the date picker: %s", edit)
		}
		if !strings.Contains(edit, `<option value="date" selected>`) {
			t.Fatalf("edit form did not preselect the date mode: %s", edit)
		}
	}
}

// A cutoff already in the past is rejected with a message, not saved as a client
// that can never log in.
func TestPastDateIsRejected(t *testing.T) {
	srv, sessions, _, _ := newAPIClientsTestServer(t)
	past := time.Now().Local().AddDate(0, 0, -1).Format("2006-01-02")
	res := authedPostForm(t, srv, sessions, "/admin/api-clients",
		createForm("Kadaluwarsa", "kadaluwarsa", url.Values{
			"expiry_choice": {"custom"}, "custom_mode": {"date"}, "custom_date": {past},
		}))
	body := res.Body.String()
	if !strings.Contains(body, "tanggal kedaluwarsa harus di masa depan") {
		t.Fatalf("past date was not rejected: %s", body)
	}
	if strings.Contains(body, "kadaluwarsa</td>") {
		t.Fatal("rejected client was created anyway")
	}
}

// A blank or zero count must not silently become some other duration.
func TestEmptyCustomCountIsRejected(t *testing.T) {
	srv, sessions, _, _ := newAPIClientsTestServer(t)
	res := authedPostForm(t, srv, sessions, "/admin/api-clients",
		createForm("Kosong", "kosong", url.Values{
			"expiry_choice": {"custom"}, "custom_mode": {"years"}, "custom_years": {""},
		}))
	if !strings.Contains(res.Body.String(), "Durasi dalam tahun harus berupa angka") {
		t.Fatalf("empty year count was not rejected: %s", res.Body.String())
	}
}

func extractEditURL(t *testing.T, body string) string {
	t.Helper()
	const marker = `href="/admin/api-clients/`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal("no client row rendered")
	}
	rest := body[i+len(`href="`):]
	return rest[:strings.Index(rest, `"`)]
}
