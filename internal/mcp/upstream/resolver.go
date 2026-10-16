package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// NormalizePhone applies the plan's normalisation: strip spaces/dashes,
// strip a leading "+", and turn a leading "0" into "62" (Indonesian local
// format). A phone already in international form passes through
// unchanged.
func NormalizePhone(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.TrimPrefix(s, "+")
	if strings.HasPrefix(s, "0") {
		s = "62" + s[1:]
	}
	return s
}

// sessionEntry is one GET /api/v1/sessions row this resolver cares about.
type sessionEntry struct {
	ID    string `json:"id"`
	Phone string `json:"phone"`
}

type cacheEntry struct {
	byPhone map[string]string // normalised phone -> session id
	at      time.Time
}

// resolver caches GET /api/v1/sessions per host for a short TTL so
// repeated phone->session_id lookups (one per tool call) don't hammer
// every host's session list.
type resolver struct {
	pool *Pool
	ttl  time.Duration
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry // host name -> cache
}

func newResolver(p *Pool, ttl time.Duration, now func() time.Time) *resolver {
	return &resolver{pool: p, ttl: ttl, now: now, cache: map[string]cacheEntry{}}
}

func (r *resolver) sessionsForHost(ctx context.Context, h *Host) (map[string]string, error) {
	r.mu.Lock()
	if e, ok := r.cache[h.Name]; ok && r.now().Sub(e.at) < r.ttl {
		r.mu.Unlock()
		return e.byPhone, nil
	}
	r.mu.Unlock()

	res, err := h.doJSON(ctx, "GET", "/api/v1/sessions", nil, nil)
	if err != nil {
		return nil, err
	}
	if res.Status != 200 {
		return nil, fmt.Errorf("host %s: GET /api/v1/sessions returned %d", h.Name, res.Status)
	}
	// GET /api/v1/sessions answers a bare JSON array of Session (see
	// openapi.yaml); a {"sessions":[...]} envelope is accepted too in case
	// a future version wraps it.
	var sessions []sessionEntry
	if err := json.Unmarshal(res.Body, &sessions); err != nil {
		var wrapped struct {
			Sessions []sessionEntry `json:"sessions"`
		}
		if err2 := json.Unmarshal(res.Body, &wrapped); err2 != nil {
			return nil, fmt.Errorf("host %s: could not parse sessions list: %w", h.Name, err)
		}
		sessions = wrapped.Sessions
	}
	byPhone := make(map[string]string, len(sessions))
	for _, s := range sessions {
		if s.Phone != "" {
			byPhone[NormalizePhone(s.Phone)] = s.ID
		}
	}
	r.mu.Lock()
	r.cache[h.Name] = cacheEntry{byPhone: byPhone, at: r.now()}
	r.mu.Unlock()
	return byPhone, nil
}

// ResolveSession resolves session_id/phone/target into one owning host and
// a session id on it:
//
//   - an explicit session_id is looked up on every host (in priority
//     order) until one is found to own it; that host is pinned.
//   - otherwise a phone (explicit, or Pool.DefaultPhone) is resolved on
//     the first host that answers (honouring target if given).
func (p *Pool) ResolveSession(ctx context.Context, sessionID, phone, target string) (host *Host, resolvedID string, err error) {
	hosts, err := p.orderedHosts(target)
	if err != nil {
		return nil, "", err
	}
	if sessionID != "" {
		for _, h := range hosts {
			byPhone, err := p.resolver.sessionsForHost(ctx, h)
			if err != nil {
				continue
			}
			for _, id := range byPhone {
				if id == sessionID {
					return h, sessionID, nil
				}
			}
		}
		if target != "" {
			// Pinned to one host: trust the caller's session_id even if the
			// cache didn't confirm it (e.g. a brand-new session not yet
			// cached) rather than erroring out.
			return hosts[0], sessionID, nil
		}
		return nil, "", fmt.Errorf("session_id %q was not found on any configured host", sessionID)
	}
	if phone == "" {
		phone = p.DefaultPhone
	}
	if phone == "" {
		return nil, "", fmt.Errorf("one of session_id or phone is required (and no WSM_MCP_DEFAULT_PHONE is set)")
	}
	norm := NormalizePhone(phone)
	var lastErr error
	for _, h := range hosts {
		byPhone, err := p.resolver.sessionsForHost(ctx, h)
		if err != nil {
			lastErr = err
			continue
		}
		if id, ok := byPhone[norm]; ok {
			return h, id, nil
		}
	}
	if lastErr != nil {
		return nil, "", fmt.Errorf("could not resolve phone %q on any host: %w", phone, lastErr)
	}
	return nil, "", fmt.Errorf("phone %q is not a known session on any configured host", phone)
}

// ResolveAll returns phone->session_id (or an error string) for every
// configured host, for wsm_resolve_session.
func (p *Pool) ResolveAll(ctx context.Context, phone string) map[string]any {
	if phone == "" {
		phone = p.DefaultPhone
	}
	norm := NormalizePhone(phone)
	out := map[string]any{}
	for _, h := range p.Hosts {
		byPhone, err := p.resolver.sessionsForHost(ctx, h)
		if err != nil {
			out[h.Name] = map[string]string{"error": err.Error()}
			continue
		}
		if id, ok := byPhone[norm]; ok {
			out[h.Name] = map[string]string{"session_id": id}
		} else {
			out[h.Name] = map[string]string{"error": "phone not found on this host"}
		}
	}
	return out
}
