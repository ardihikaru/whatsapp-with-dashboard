package handler

// The expiry half of the /admin/api-clients form: turning the "Masa berlaku
// access token" controls into apiclient.TokenSettings and back again. Split from
// api_clients_form.go, which keeps the plain name/login/description mapping, so
// both files stay inside the 200 LoC rule.

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
)

// A "month" is 30 days and a "year" 365, matching the 30-day preset this form
// has always offered. Calendar-exact months would make the stored TTL depend on
// WHEN a token is issued, which the seconds column cannot express — the cure for
// callers who need a precise cutoff is the date mode, not a fuzzier duration.
const (
	secondsPerDay   = 86400
	secondsPerMonth = 30 * secondsPerDay
	secondsPerYear  = 365 * secondsPerDay
)

// dateLayout is what <input type="date"> submits.
const dateLayout = "2006-01-02"

// presetTTLs are the fixed choices in the expiry dropdown, in seconds, mapped to
// their labels. A value not in here is not a preset, so it round-trips through
// the "Durasi khusus" controls instead.
var presetTTLs = map[int]string{
	86400:              "1 hari",
	604800:             "7 hari",
	2592000:            "30 hari",
	secondsPerYear:     "1 tahun",
	2 * secondsPerYear: "2 tahun",
	3 * secondsPerYear: "3 tahun",
	4 * secondsPerYear: "4 tahun",
	5 * secondsPerYear: "5 tahun",
}

// defaultExpiryChoice is the dropdown value a blank form starts on.
const defaultExpiryChoice = "86400"

// tokenSettings maps the submitted form onto the domain's expiry policy.
func (f apiClientForm) tokenSettings() (apiclient.TokenSettings, error) {
	switch f.ExpiryChoice {
	case "no_expiry":
		return apiclient.TokenSettings{NoExpiry: true}, nil
	case "custom":
		return f.customSettings()
	default:
		n, err := strconv.Atoi(f.ExpiryChoice)
		if err != nil || presetTTLs[n] == "" {
			n, _ = strconv.Atoi(defaultExpiryChoice)
		}
		return apiclient.TokenSettings{TTLSeconds: &n}, nil
	}
}

// customSettings resolves the "Durasi khusus" sub-form. Each mode reads exactly
// one input; the template disables the others so a stale value in a hidden field
// can never be the one that counts.
func (f apiClientForm) customSettings() (apiclient.TokenSettings, error) {
	switch f.CustomMode {
	case "date":
		t, err := parseExpiryDate(f.CustomDate)
		if err != nil {
			return apiclient.TokenSettings{}, err
		}
		return apiclient.TokenSettings{ExpiresAt: &t}, nil
	case "years":
		n, err := positiveCount(f.CustomYears, 100, "Durasi dalam tahun")
		if err != nil {
			return apiclient.TokenSettings{}, err
		}
		n *= secondsPerYear
		return apiclient.TokenSettings{TTLSeconds: &n}, nil
	case "months":
		n, err := positiveCount(f.CustomMonths, 1200, "Durasi dalam bulan")
		if err != nil {
			return apiclient.TokenSettings{}, err
		}
		n *= secondsPerMonth
		return apiclient.TokenSettings{TTLSeconds: &n}, nil
	case "seconds":
		n, err := positiveCount(f.CustomTTLSeconds, 0, "Durasi dalam detik")
		if err != nil {
			return apiclient.TokenSettings{}, err
		}
		return apiclient.TokenSettings{TTLSeconds: &n}, nil
	default:
		return apiclient.TokenSettings{}, fmt.Errorf("pilih jenis durasi khusus terlebih dahulu")
	}
}

// parseExpiryDate reads a YYYY-MM-DD picker value as the END of that day in the
// server's local zone: an admin who types 2027-12-31 means "still works on the
// 31st", not "dies at midnight as the 31st begins".
func parseExpiryDate(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("tanggal kedaluwarsa harus diisi")
	}
	day, err := time.ParseInLocation(dateLayout, value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("tanggal kedaluwarsa tidak valid")
	}
	end := day.AddDate(0, 0, 1).Add(-time.Second).UTC()
	if !time.Now().UTC().Before(end) {
		return time.Time{}, fmt.Errorf("tanggal kedaluwarsa harus di masa depan")
	}
	return end, nil
}

// positiveCount parses one of the numeric custom inputs. max <= 0 means no upper
// bound; the bounds exist so a typo like 99999 tahun cannot overflow the stored
// seconds into something meaningless.
func positiveCount(value string, max int, label string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s harus berupa angka yang lebih besar dari 0", label)
	}
	if max > 0 && n > max {
		return 0, fmt.Errorf("%s maksimal %d", label, max)
	}
	return n, nil
}

// expiryFormFromClient prefills the expiry controls from a stored client. Any
// TTL that is not a preset comes back through "Durasi khusus" in the largest
// unit that divides it exactly, so a value saved as 3 tahun re-opens as 3 tahun
// rather than as 94608000 detik.
func expiryFormFromClient(c apiclient.Client, form *apiClientForm) {
	form.CustomMode = "date"
	switch {
	case c.NoExpiry:
		form.ExpiryChoice = "no_expiry"
	case c.TokenExpiresAt != nil:
		form.ExpiryChoice = "custom"
		form.CustomDate = c.TokenExpiresAt.Local().Format(dateLayout)
	case c.TokenTTLSeconds == nil:
		form.ExpiryChoice = defaultExpiryChoice
	case presetTTLs[*c.TokenTTLSeconds] != "":
		form.ExpiryChoice = strconv.Itoa(*c.TokenTTLSeconds)
	default:
		form.ExpiryChoice = "custom"
		ttl := *c.TokenTTLSeconds
		switch {
		case ttl%secondsPerYear == 0:
			form.CustomMode, form.CustomYears = "years", strconv.Itoa(ttl/secondsPerYear)
		case ttl%secondsPerMonth == 0:
			form.CustomMode, form.CustomMonths = "months", strconv.Itoa(ttl/secondsPerMonth)
		default:
			form.CustomMode, form.CustomTTLSeconds = "seconds", strconv.Itoa(ttl)
		}
	}
}

// expiryLabel is the human summary shown in the admin table.
func expiryLabel(c apiclient.Client) string {
	if c.NoExpiry {
		return "Tanpa kedaluwarsa"
	}
	if c.TokenExpiresAt != nil {
		return "Sampai " + c.TokenExpiresAt.Local().Format(dateLayout)
	}
	if c.TokenTTLSeconds == nil {
		return presetTTLs[86400]
	}
	ttl := *c.TokenTTLSeconds
	if label := presetTTLs[ttl]; label != "" {
		return label
	}
	switch {
	case ttl%secondsPerYear == 0:
		return fmt.Sprintf("%d tahun", ttl/secondsPerYear)
	case ttl%secondsPerMonth == 0:
		return fmt.Sprintf("%d bulan", ttl/secondsPerMonth)
	default:
		return (time.Duration(ttl) * time.Second).String()
	}
}
