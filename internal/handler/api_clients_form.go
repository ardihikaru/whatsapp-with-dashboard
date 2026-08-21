package handler

// Form parsing and view mapping for /admin/api-clients. Split out of
// api_clients.go to keep both files inside the 200 LoC rule. The expiry
// controls, which carry all the real branching, live in api_clients_expiry.go.

import (
	"net/http"
	"strings"

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
		CustomMode:       r.FormValue("custom_mode"),
		CustomDate:       strings.TrimSpace(r.FormValue("custom_date")),
		CustomYears:      strings.TrimSpace(r.FormValue("custom_years")),
		CustomMonths:     strings.TrimSpace(r.FormValue("custom_months")),
		CustomTTLSeconds: strings.TrimSpace(r.FormValue("custom_ttl_seconds")),
	}
}

func formFromClient(c apiclient.Client) apiClientForm {
	form := apiClientForm{
		Name:        c.Name,
		LoginID:     c.LoginID,
		Description: c.Description,
	}
	expiryFormFromClient(c, &form)
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
