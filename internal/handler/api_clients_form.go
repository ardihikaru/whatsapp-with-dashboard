package handler

// Form parsing and view mapping for /admin/api-clients. Split out of
// api_clients.go to keep both files inside the 200 LoC rule.

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
)

func apiClientFormFromRequest(r *http.Request) apiClientForm {
	_ = r.ParseForm()
	return formFromPostOrEmpty(r)
}

func formFromPostOrEmpty(r *http.Request) apiClientForm {
	return apiClientForm{
		Name:             strings.TrimSpace(r.FormValue("name")),
		LoginID:          strings.TrimSpace(r.FormValue("login_id")),
		Description:      strings.TrimSpace(r.FormValue("description")),
		ExpiryChoice:     r.FormValue("expiry_choice"),
		CustomTTLSeconds: strings.TrimSpace(r.FormValue("custom_ttl_seconds")),
	}
}

func (f apiClientForm) tokenSettings() (apiclient.TokenSettings, error) {
	switch f.ExpiryChoice {
	case "no_expiry":
		return apiclient.TokenSettings{NoExpiry: true}, nil
	case "custom":
		n, err := strconv.Atoi(f.CustomTTLSeconds)
		if err != nil || n <= 0 {
			return apiclient.TokenSettings{}, fmt.Errorf("durasi khusus harus berupa jumlah detik yang positif")
		}
		return apiclient.TokenSettings{TTLSeconds: &n}, nil
	case "86400", "604800", "2592000":
		n, _ := strconv.Atoi(f.ExpiryChoice)
		return apiclient.TokenSettings{TTLSeconds: &n}, nil
	default:
		n := 86400
		return apiclient.TokenSettings{TTLSeconds: &n}, nil
	}
}

func formFromClient(c apiclient.Client) apiClientForm {
	form := apiClientForm{
		Name:        c.Name,
		LoginID:     c.LoginID,
		Description: c.Description,
	}
	if c.NoExpiry {
		form.ExpiryChoice = "no_expiry"
		return form
	}
	if c.TokenTTLSeconds == nil {
		form.ExpiryChoice = "86400"
		return form
	}
	switch *c.TokenTTLSeconds {
	case 86400, 604800, 2592000:
		form.ExpiryChoice = strconv.Itoa(*c.TokenTTLSeconds)
	default:
		form.ExpiryChoice = "custom"
		form.CustomTTLSeconds = strconv.Itoa(*c.TokenTTLSeconds)
	}
	return form
}

func viewClients(clients []apiclient.Client) []apiClientView {
	out := make([]apiClientView, 0, len(clients))
	for _, c := range clients {
		out = append(out, viewClient(c))
	}
	return out
}

func viewClient(c apiclient.Client) apiClientView {
	return apiClientView{
		ID: c.ID, Name: c.Name, LoginID: c.LoginID, Description: c.Description,
		Active: c.Active, ExpiryLabel: expiryLabel(c),
		CreatedAt: c.CreatedAt.Local().Format("2006-01-02 15:04"),
	}
}

func expiryLabel(c apiclient.Client) string {
	if c.NoExpiry {
		return "Tanpa kedaluwarsa"
	}
	if c.TokenTTLSeconds == nil {
		return "1 hari"
	}
	d := time.Duration(*c.TokenTTLSeconds) * time.Second
	switch *c.TokenTTLSeconds {
	case 86400:
		return "1 hari"
	case 604800:
		return "7 hari"
	case 2592000:
		return "30 hari"
	default:
		return d.String()
	}
}
