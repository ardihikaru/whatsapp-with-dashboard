package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
)

// apiClientPageSize is the admin table page size.
const apiClientPageSize = 20

type apiClientsPageData struct {
	PageBase
	Clients []apiClientView
	Search  string
	Pager   pagerView
	Form    apiClientForm
	Error   string
	Secret  string
}

type apiClientEditPageData struct {
	PageBase
	Client apiClientView
	Form   apiClientForm
	Error  string
	Secret string
}

type apiClientView struct {
	ID          string
	Name        string
	LoginID     string
	Description string
	Active      bool
	ExpiryLabel string
	CreatedAt   string
}

// apiClientForm is the raw admin form, kept as strings so a rejected submission
// can be re-rendered exactly as it was typed. CustomMode selects which of the
// four Custom* fields the expiry parser reads; see api_clients_expiry.go.
type apiClientForm struct {
	Name             string
	LoginID          string
	Description      string
	ExpiryChoice     string
	CustomMode       string
	CustomDate       string
	CustomYears      string
	CustomMonths     string
	CustomTTLSeconds string
}

// newAPIClientForm is the blank create form: the default expiry preset with the
// custom sub-form parked on its first mode.
func newAPIClientForm() apiClientForm {
	return apiClientForm{ExpiryChoice: defaultExpiryChoice, CustomMode: "date"}
}

func (h *handlers) GetAPIClients(w http.ResponseWriter, r *http.Request) {
	h.renderAPIClients(w, r, newAPIClientForm(), "", "")
}

func (h *handlers) PostAPIClientCreate(w http.ResponseWriter, r *http.Request) {
	form := apiClientFormFromRequest(r)
	settings, err := form.tokenSettings()
	if err != nil {
		h.renderAPIClients(w, r, form, err.Error(), "")
		return
	}
	created, err := h.APIClients.Create(r.Context(), form.Name, form.LoginID, form.Description, settings)
	if err != nil {
		h.renderAPIClients(w, r, form, err.Error(), "")
		return
	}
	h.renderAPIClients(w, r, newAPIClientForm(), "", created.Secret)
}

func (h *handlers) GetAPIClientEdit(w http.ResponseWriter, r *http.Request) {
	c, err := h.APIClients.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.renderAPIClientEdit(w, r, c, formFromClient(c), "", "")
}

func (h *handlers) PostAPIClientEdit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	form := apiClientFormFromRequest(r)
	settings, err := form.tokenSettings()
	if err != nil {
		h.renderAPIClientEditFallback(w, r, id, form, err.Error(), "")
		return
	}
	if err := h.APIClients.Update(r.Context(), id, form.Name, form.LoginID, form.Description, settings); err != nil {
		h.renderAPIClientEditFallback(w, r, id, form, err.Error(), "")
		return
	}
	http.Redirect(w, r, h.BasePath+"/admin/api-clients", http.StatusSeeOther)
}

func (h *handlers) PostAPIClientActivate(w http.ResponseWriter, r *http.Request) {
	h.toggleAPIClient(w, r, true)
}

func (h *handlers) PostAPIClientDeactivate(w http.ResponseWriter, r *http.Request) {
	h.toggleAPIClient(w, r, false)
}

func (h *handlers) PostAPIClientRotate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	secret, err := h.APIClients.RotateSecret(r.Context(), id)
	if err != nil {
		h.renderAPIClientEditFallback(w, r, id, formFromPostOrEmpty(r), err.Error(), "")
		return
	}
	c, err := h.APIClients.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.renderAPIClientEdit(w, r, c, formFromClient(c), "", secret)
}

func (h *handlers) PostAPIClientDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.APIClients.Delete(r.Context(), r.PathValue("id")); err != nil && !errors.Is(err, apiclient.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.Redirect(w, r, h.BasePath+"/admin/api-clients", http.StatusSeeOther)
}

func (h *handlers) toggleAPIClient(w http.ResponseWriter, r *http.Request, active bool) {
	if err := h.APIClients.SetActive(r.Context(), r.PathValue("id"), active); err != nil && !errors.Is(err, apiclient.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.Redirect(w, r, h.BasePath+"/admin/api-clients", http.StatusSeeOther)
}

func (h *handlers) renderAPIClients(w http.ResponseWriter, r *http.Request, form apiClientForm, errMsg, secret string) {
	ctx := r.Context()
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	page := pageParam(r.URL.Query().Get("page"))

	total, err := h.APIClients.CountList(ctx, search)
	if err != nil {
		errMsg = "Gagal memuat daftar API client."
	}
	pager := newPager(h.BasePath+"/admin/api-clients", searchValues(search), page, apiClientPageSize, total)
	clients, err := h.APIClients.ListPage(ctx, search, apiClientPageSize, (pager.Page-1)*apiClientPageSize)
	if err != nil {
		errMsg = "Gagal memuat daftar API client."
	}

	data := apiClientsPageData{
		Clients: viewClients(clients), Search: search, Pager: pager,
		Form: form, Error: errMsg, Secret: secret,
	}
	data.PageBase = h.newPageBase(r, "API Client", "/admin/api-clients")
	h.Renderer.RenderPage(w, "api_clients/list", data)
}

// searchValues carries the active search across pager links.
func searchValues(search string) url.Values {
	v := url.Values{}
	if search != "" {
		v.Set("q", search)
	}
	return v
}

func (h *handlers) renderAPIClientEditFallback(w http.ResponseWriter, r *http.Request, id string, form apiClientForm, errMsg, secret string) {
	c, err := h.APIClients.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.renderAPIClientEdit(w, r, c, form, errMsg, secret)
}

func (h *handlers) renderAPIClientEdit(w http.ResponseWriter, r *http.Request, c apiclient.Client, form apiClientForm, errMsg, secret string) {
	data := apiClientEditPageData{Client: viewClient(c), Form: form, Error: errMsg, Secret: secret}
	data.PageBase = h.newPageBase(r, "Edit API Client", "/admin/api-clients")
	h.Renderer.RenderPage(w, "api_clients/edit", data)
}
