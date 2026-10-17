// t_waxum_contacts.go: the bespoke waxum_contacts tool. With q=="" (or
// absent), this is the exact passthrough waxumSpec would have generated —
// one call to waxum.Client.Contacts, unranked, unchanged from before this
// file existed. With q set, it instead:
//
//  1. fetches this session's FULL synced waxum contact list (paging at
//     1000 per call until Total is reached, then caching that per
//     host+session for 60s — waxum's rate-limit burst is tiny, see
//     CLAUDE.md's rate-limit hazard, and q itself is applied client-side
//     below rather than by waxum, so every distinct q for the same
//     session reuses one fetch);
//  2. fetches WSM's own owner-managed contact book from the SAME resolved
//     host (GET /api/v1/contacts, uninstrumented — a failure here is
//     noted in a "warnings" field but does not fail the call, since
//     waxum's own contacts are still a useful answer on their own);
//  3. ranks BOTH lists together with internal/contactmatch, over each
//     side's own name-shaped fields (wsm: name + aliases; waxum:
//     full_name, first_name, push_name, business_name, phone);
//  4. dedupes by jid (falling back to phone when neither side has a jid)
//     — an owner contact that waxum also has synced wins the slot, since
//     it is the operator's own curated name for that number; and
//  5. applies limit/offset to the RANKED result.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/contactmatch"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// wsmContactWire mirrors internal/handler/api_contacts_crud.go's apiContact
// wire shape closely enough to decode it — kept as its own (smaller) type
// here rather than importing internal/handler, which pulls in the whole
// HTTP handler package for one JSON shape.
type wsmContactWire struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Phone   string   `json:"phone"`
	Aliases []string `json:"aliases"`
	JID     string   `json:"jid"`
}

// waxumContactsCacheEntry/waxumContactsCache: the 60s-per-host+session
// cache of a session's FULL waxum contact list, with an injectable clock
// so tests do not need to sleep.
type waxumContactsCacheEntry struct {
	contacts []waxum.StoredContact
	at       time.Time
}

type waxumContactsCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	byKey map[string]waxumContactsCacheEntry
}

func newWaxumContactsCache() *waxumContactsCache {
	return &waxumContactsCache{ttl: 60 * time.Second, now: time.Now, byKey: map[string]waxumContactsCacheEntry{}}
}

func (c *waxumContactsCache) get(key string) ([]waxum.StoredContact, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byKey[key]
	if !ok || c.now().Sub(e.at) >= c.ttl {
		return nil, false
	}
	return e.contacts, true
}

func (c *waxumContactsCache) set(key string, contacts []waxum.StoredContact) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byKey[key] = waxumContactsCacheEntry{contacts: contacts, at: c.now()}
}

// fetchAllWaxumContacts pages waxum's GET /sessions/{id}/contacts (q="",
// so waxum does no filtering of its own) at 1000 per page until Total is
// reached, caching the result against cache for 60s.
func fetchAllWaxumContacts(ctx context.Context, p *upstream.Pool, cache *waxumContactsCache, hostName, sessionID string) ([]waxum.StoredContact, error) {
	key := hostName + "|" + sessionID
	if cached, ok := cache.get(key); ok {
		return cached, nil
	}
	const pageSize = 1000
	var all []waxum.StoredContact
	offset := 0
	for {
		res, err := p.CallWaxum(ctx, "Contacts", false, hostName, sessionID, "", pageSize, offset)
		if err != nil {
			return nil, err
		}
		page, ok := res.Data.(waxum.ContactsPage)
		if !ok {
			return nil, fmt.Errorf("waxum_contacts: unexpected Contacts() return type %T", res.Data)
		}
		all = append(all, page.Contacts...)
		offset += len(page.Contacts)
		if len(page.Contacts) == 0 || int64(offset) >= page.Total {
			break
		}
	}
	cache.set(key, all)
	return all, nil
}

// fetchWSMContacts reads WSM's own owner contact book from the SAME
// resolved host (never a different one — a ranked search must reflect
// what THIS host's operator actually saved).
func fetchWSMContacts(ctx context.Context, p *upstream.Pool, hostName string) ([]wsmContactWire, error) {
	res, err := p.CallWSM(ctx, "GET", "/api/v1/contacts", nil, nil, nil, false, hostName)
	if err != nil {
		return nil, err
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("GET /api/v1/contacts on host %s returned %d", hostName, res.Status)
	}
	var wrapped struct {
		Contacts []wsmContactWire `json:"contacts"`
	}
	if err := json.Unmarshal(res.Body, &wrapped); err != nil {
		return nil, fmt.Errorf("decode GET /api/v1/contacts: %w", err)
	}
	return wrapped.Contacts, nil
}

// rankedContactOut is one item of the ranked waxum_contacts output —
// StoredContact's own snake_case fields (so an unranked caller keying off
// the pre-existing shape still finds jid/phone/full_name/... unchanged)
// plus chat_jid, match_score, matched_field, source and name.
type rankedContactOut struct {
	JID          string  `json:"jid,omitempty"`
	Phone        *string `json:"phone,omitempty"`
	LIDJID       *string `json:"lid_jid,omitempty"`
	FullName     *string `json:"full_name,omitempty"`
	FirstName    *string `json:"first_name,omitempty"`
	PushName     *string `json:"push_name,omitempty"`
	BusinessName *string `json:"business_name,omitempty"`
	ChatJID      string  `json:"chat_jid,omitempty"`
	Name         string  `json:"name"`
	MatchScore   int     `json:"match_score"`
	MatchedField string  `json:"matched_field"`
	Source       string  `json:"source"` // "wsm" | "waxum"
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// displayName picks the best available label for a waxum StoredContact —
// FullName, then PushName, then FirstName, then BusinessName, then Phone —
// so rankedContactOut.Name is never empty just because FullName is nil.
func waxumDisplayName(c waxum.StoredContact) string {
	for _, p := range []*string{c.FullName, c.PushName, c.FirstName, c.BusinessName} {
		if p != nil && *p != "" {
			return *p
		}
	}
	return deref(c.Phone)
}

// dedupeKey is the identity a ranked result is deduplicated on: jid when
// either side has one, else phone. Returns "" when neither is available,
// in which case the candidate is never deduplicated against anything.
func dedupeKey(jid, phone string) string {
	if jid != "" {
		return "jid:" + jid
	}
	if phone != "" {
		return "phone:" + phone
	}
	return ""
}

type scoredCandidate struct {
	out   rankedContactOut
	key   string
	score int
}

// rankContacts scores every wsm + waxum candidate against q, dedupes by
// dedupeKey (a wsm entry displaces a waxum entry sharing the same key —
// "owner contacts win ties and dedupe"), and returns them sorted highest
// score first, wsm-sourced winning an exact score tie.
func rankContacts(q string, wsmContacts []wsmContactWire, waxumContacts []waxum.StoredContact) []rankedContactOut {
	var scored []scoredCandidate

	for _, c := range wsmContacts {
		fields := []contactmatch.Field{{Name: "name", Value: c.Name}}
		for _, a := range c.Aliases {
			if a == "" {
				continue
			}
			fields = append(fields, contactmatch.Field{Name: "alias", Value: a, Alias: true})
		}
		res, ok := contactmatch.Match(q, fields)
		if !ok {
			continue
		}
		out := rankedContactOut{
			JID: c.JID, Phone: strPtr(c.Phone), ChatJID: c.JID,
			Name: c.Name, MatchScore: res.Score, MatchedField: res.Field, Source: "wsm",
		}
		scored = append(scored, scoredCandidate{out: out, key: dedupeKey(c.JID, c.Phone), score: res.Score})
	}

	for _, c := range waxumContacts {
		fields := []contactmatch.Field{
			{Name: "full_name", Value: deref(c.FullName)},
			{Name: "first_name", Value: deref(c.FirstName)},
			{Name: "push_name", Value: deref(c.PushName)},
			{Name: "business_name", Value: deref(c.BusinessName)},
			{Name: "phone", Value: deref(c.Phone)},
		}
		res, ok := contactmatch.Match(q, fields)
		if !ok {
			continue
		}
		out := rankedContactOut{
			JID: c.JID, Phone: c.Phone, LIDJID: c.LIDJID, FullName: c.FullName, FirstName: c.FirstName,
			PushName: c.PushName, BusinessName: c.BusinessName, ChatJID: c.JID,
			Name: waxumDisplayName(c), MatchScore: res.Score, MatchedField: res.Field, Source: "waxum",
		}
		scored = append(scored, scoredCandidate{out: out, key: dedupeKey(c.JID, deref(c.Phone)), score: res.Score})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		// Owner contacts win ties.
		iWSM, jWSM := scored[i].out.Source == "wsm", scored[j].out.Source == "wsm"
		if iWSM != jWSM {
			return iWSM
		}
		return scored[i].out.Name < scored[j].out.Name
	})

	seen := map[string]bool{}
	out := make([]rankedContactOut, 0, len(scored))
	for _, sc := range scored {
		if sc.key != "" {
			if seen[sc.key] {
				continue
			}
			seen[sc.key] = true
		}
		out = append(out, sc.out)
	}
	return out
}

// sharedWaxumContactsCache is created once at package init (not per-Tool-
// build-call, of which there is exactly one anyway) so every call through
// the registry's one waxum_contacts tool shares the same 60s cache.
var sharedWaxumContactsCache = newWaxumContactsCache()

// waxumContactsTool builds the bespoke waxum_contacts Tool. WaxumMethod
// stays "Contacts" (not Route) so sync_test.go's
// TestEveryWaxumClientMethodHasAnMCPTool still sees waxum.Client.Contacts
// covered, exactly as createSessionTool keeps WaxumMethod: "CreateSession"
// for the same reason.
func waxumContactsTool() Tool {
	return Tool{
		Name: "waxum_contacts", WaxumMethod: "Contacts", ReadOnly: true, Idempotent: true,
		Desc: "List a session's synced WhatsApp contacts. With q empty, an unranked passthrough to waxum's own GET .../contacts (jid, phone, names). With q set, ranks BOTH waxum's synced contacts AND WSM's own owner-managed contact book (wsm_contacts_list) together via name/alias matching, owner contacts winning ties and de-duplicated against a matching waxum contact by jid/phone — so a saved name like \"Ria\" ranks above an unrelated synced contact that merely contains that text. Use to turn a person's name into a chat_jid.",
		Params: []Param{
			sessionIDParam, phoneParam,
			{Name: "q", Type: "string", Desc: "Optional name/phone filter. Empty is an unranked passthrough; set, it ranks across WSM owner contacts + waxum synced contacts together."},
			{Name: "limit", Type: "integer", Desc: "Page size (default 50, max 1000)."},
			{Name: "offset", Type: "integer", Desc: "Rows to skip (default 0), applied to the RANKED result when q is set."},
			targetParam,
		},
		Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
			host, sid, err := resolveWaxum(ctx, p, a)
			if err != nil {
				return nil, err
			}
			q := strings.TrimSpace(a.Str("q"))
			limit := a.Int("limit", 50)
			offset := a.Int("offset", 0)

			if q == "" {
				res, err := p.CallWaxum(ctx, "Contacts", false, host, sid, "", limit, offset)
				if err != nil {
					return nil, err
				}
				return waxumResultOut(res), nil
			}

			waxumContacts, err := fetchAllWaxumContacts(ctx, p, sharedWaxumContactsCache, host, sid)
			if err != nil {
				return nil, err
			}
			var warnings []string
			wsmContacts, wsmErr := fetchWSMContacts(ctx, p, host)
			if wsmErr != nil {
				warnings = append(warnings, "could not fetch WSM owner contacts: "+wsmErr.Error())
			}

			ranked := rankContacts(q, wsmContacts, waxumContacts)
			total := len(ranked)
			if offset > total {
				offset = total
			}
			end := offset + limit
			if limit <= 0 || end > total {
				end = total
			}
			page := ranked[offset:end]

			data := map[string]any{
				"contacts": page, "total": total, "limit": limit, "offset": offset, "query": q,
			}
			if len(warnings) > 0 {
				data["warnings"] = warnings
			}
			return map[string]any{"host": host, "data": data}, nil
		},
	}
}
