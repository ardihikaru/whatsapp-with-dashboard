package sessionalert

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// membershipCacheTTL bounds how long a session's joined-group list is
// trusted before Member re-fetches it. Member is reachable ONLY from
// chooseSender's final fallback tier (sender.go's own doc comment), so
// this is not latency-sensitive — what matters is staying far above
// waxum's tiny rate-limit burst (CLAUDE.md's rate-limit hazard: a burst
// of only ~3 requests before a 429) even when several sessions go
// logged_out in the same batch and each is tried as a fallback candidate.
// 10 minutes is generous for a value that changes only when an operator
// adds/removes the paired device from a WhatsApp group, which is rare and
// not time-critical to notice.
const membershipCacheTTL = 10 * time.Minute

// managerGateway is the production Gateway, a thin adapter over
// *wa.Manager — mirrors internal/groupbroadcast/gateway_wa.go's
// managerGateway.
type managerGateway struct {
	m *wa.Manager

	now func() time.Time // injected for gateway_wa_test.go; defaults to time.Now
	ttl time.Duration    // injected for gateway_wa_test.go; defaults to membershipCacheTTL

	mu    sync.Mutex
	cache map[string]membershipCacheEntry // session ID -> cached joined-group JIDs
}

type membershipCacheEntry struct {
	jids      map[string]bool
	fetchedAt time.Time
}

// NewManagerGateway wraps m as a Gateway.
func NewManagerGateway(m *wa.Manager) Gateway {
	return &managerGateway{m: m, now: time.Now, ttl: membershipCacheTTL, cache: map[string]membershipCacheEntry{}}
}

func (g *managerGateway) Snapshots() []Snapshot {
	snaps := g.m.Snapshots()
	out := make([]Snapshot, 0, len(snaps))
	for _, sn := range snaps {
		out = append(out, Snapshot{
			ID: sn.ID, Name: sn.Name, Phone: sn.Phone,
			Connected: sn.Connected, Active: sn.Active,
			State: string(sn.State), StalledReason: sn.StalledReason,
			SelfReminder: sn.SelfReminder,
		})
	}
	return out
}

// Send dispatches text to groupJID through sessionID, stamped with
// message.CategorySessionAlert — the SAME destination-agnostic send path
// internal/groupbroadcast/gateway_wa.go's own Send uses, via
// wa.Manager.Dispatch and wa.ParseGroupJID.
func (g *managerGateway) Send(ctx context.Context, sessionID, groupJID, text string) error {
	s, ok := g.m.Get(sessionID)
	if !ok {
		return wa.ErrNotFound
	}
	jid, err := wa.ParseGroupJID(groupJID)
	if err != nil {
		return fmt.Errorf("sessionalert: invalid target group jid %q: %w", groupJID, err)
	}
	msg := &message.Message{
		ID:        wa.NewID(),
		SessionID: s.ID,
		ToPhone:   jid.String(),
		Body:      text,
		Status:    message.StatusQueued,
		Category:  message.CategorySessionAlert,
	}
	dr, err := g.m.Dispatch(ctx, s, msg, jid, nil, 0)
	if err != nil {
		return err
	}
	if dr.Result == nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return errors.New("sessionalert: dispatch returned no result and no error")
	}
	return dr.Result.Err
}

// Member answers chooseSender's fallback-tier membership check, caching
// each session's joined-group set for g.ttl — see that field's doc
// comment for why. A session this gateway does not know, or a backend
// error fetching the live list, is reported as an ERROR (not "not a
// member"): chooseSender treats any error here as ineligible, which is
// the fail-closed direction — see sender.go's own doc comment on exactly
// this point.
func (g *managerGateway) Member(ctx context.Context, sessionID, groupJID string) (bool, error) {
	now := g.now()
	g.mu.Lock()
	entry, cached := g.cache[sessionID]
	g.mu.Unlock()
	if !cached || now.Sub(entry.fetchedAt) >= g.ttl {
		s, ok := g.m.Get(sessionID)
		if !ok {
			return false, wa.ErrNotFound
		}
		groups, err := s.JoinedGroups(ctx)
		if err != nil {
			return false, fmt.Errorf("sessionalert: fetch joined groups for %s: %w", sessionID, err)
		}
		jids := make(map[string]bool, len(groups))
		for _, gr := range groups {
			jids[gr.JID] = true
		}
		entry = membershipCacheEntry{jids: jids, fetchedAt: now}
		g.mu.Lock()
		g.cache[sessionID] = entry
		g.mu.Unlock()
	}
	return entry.jids[groupJID], nil
}

// RecordEvent forwards to wa.Manager.RecordEvent — see that method's own
// doc comment (internal/wa/manager.go) for the system-log page it feeds.
func (g *managerGateway) RecordEvent(level, note string) {
	g.m.RecordEvent(level, note)
}
