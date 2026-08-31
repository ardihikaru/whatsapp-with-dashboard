package wa

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/inbound"
)

// This file is internal/wa's INGEST side of GET /api/v1/inbound (Stage 2 of
// docs/brief-inbound-api.md — the HTTP handler that reads what this writes
// is Stage 3, in internal/handler, not here). A single fleet-wide poller
// (one goroutine, mirroring backend_waxum_events.go's eventPump — NOT one
// goroutine per session) walks every CONNECTED session's newest waxum
// message history each tick via the MessageHistory capability
// (capabilities.go), drops rows that would be a BUG downstream if stored
// (see handleInboundMessage), resolves each new @lid sender to a phone
// number (cached — one waxum call per new sender, not per message),
// stamps a matching outbound wa_messages row's client_ref/tenant_ref when
// one exists inside the configured window, and persists via
// Manager.inbound.
//
// EVERY step past "the manager is wired for this at all" is best-effort:
// this pump must never propagate an error anywhere, and per the brief must
// never log a phone number or a message BODY — the message id is enough to
// trace a problem, and a customer's own words and number are not this
// gateway's to put in a log file. A reply lost to a swallowed error is a
// smaller cost than a pump that stops running because one waxum response
// was malformed.
//
// Started from waxumBackend's own Start(*Manager) hook
// (backend_waxum_events.go), the SAME lifecycle hook the SSE event pump
// uses — see ensureInboundPump's doc comment for why that hook, not a
// bespoke one, is the right place. Runs on m.supCtx (manager-wide, not
// keyed to any one session — cancelled by Close BEFORE per-session
// teardown, same reasoning as the event pump) and is wg-tracked via m.go1
// so Close's m.wg.Wait() actually waits for it — see
// TestInboundPumpExitsWhenTheManagerCloses, mirroring
// TestManagerCloseTerminatesTheEventPump's own "the single most important
// test" framing for the same untracked-goroutine hazard.

// lidServer is waxum's own "@lid" JID server string — a WhatsApp
// linked-identity id, NOT a phone number. See splitSenderJID and
// resolveSenderPhone's doc comments for the trap this exists to name: on
// every observed live inbound row, sender_jid was a @lid (see
// migrations/0006_inbound_messages.sql's header), so treating this branch
// as an edge case would be exactly backwards.
const lidServer = "lid"

const (
	// inboundPageSize is the page size requested on every call to
	// MessageHistory.RecentMessages — see that method's (and waxum's own
	// SessionMessages') doc comment for why this is passed through
	// verbatim rather than raised further: a normal tick, with nothing
	// backlogged, is exactly one request per connected session at this
	// size.
	inboundPageSize = 100
	// inboundMaxPages bounds how far ONE session's backlog-drain can walk
	// in a single tick. After an outage this pump must catch up, but
	// following next_cursor unboundedly would let one session's backlog
	// starve every other session's tick — and this pump's own share of
	// waxum's small per-IP rate-limit budget (CLAUDE.md's waxum
	// rate-limit hazard note) — indefinitely. Whatever remains beyond 5
	// pages (500 messages at inboundPageSize) is picked up on the NEXT
	// tick instead; see processSessionInbound's "every row on the page
	// was new" check for when this cap actually bites (never on a normal
	// tick with no backlog).
	inboundMaxPages = 5
)

// ensureInboundPump starts the fleet-wide inbound-message pump exactly
// once, and only when there is something for it to do: m.inbound is nil
// for every Manager built in a test that has no use for it (see that
// field's own doc comment — cmd/app/main.go is the only production caller
// that wires a real one), and WSM_INBOUND_ENABLED lets an operator turn
// the whole thing off without touching code. Called from waxumBackend's
// own Start(*Manager) hook (backend_waxum_events.go) — the same optional
// lifecycle hook NewManager already checks for (manager.go's
// `backend.(interface{ Start(*Manager) })` branch) — rather than a second,
// bespoke hook: this pump has exactly the same "manager-wide, starts once,
// torn down by supCancel + m.wg.Wait()" shape as the SSE event pump
// already has a hook for, and meowBackend (which implements no
// MessageHistory at all — TestTheWhatsmeowBackendSatisfiesNoCapabilityInterface)
// simply never calls it, since it has no Start method either.
func (m *Manager) ensureInboundPump() {
	if m.inbound == nil || !m.cfg.InboundEnabled {
		return
	}
	m.inboundPumpOnce.Do(func() {
		m.go1(func() { m.inboundPump(m.supCtx) })
	})
}

// pokeInbound wakes the inbound pump's NEXT tick early — a non-blocking
// send (m.inboundPoke is buffered 1, exactly like waxumSessionState.poke),
// so a burst of "message" SSE events (plausibly one per inbound message on
// a busy session) never blocks its caller, backend_waxum_events.go's
// handleEvent.
//
// This function MUST NOT touch session state, call applyStatus, or poke
// statusPump — read backend_waxum_events.go's package doc comment (lines
// 21-49) before changing this. Persisting an inbound message is not
// session state, so calling this from handleEvent is compatible with that
// rule, but this function itself stays entirely off that code path: it
// does exactly one thing, a non-blocking channel send.
func (m *Manager) pokeInbound() {
	select {
	case m.inboundPoke <- struct{}{}:
	default:
	}
}

// inboundPump is the pump's main loop: poll every m.cfg.InboundPollInterval
// (or sooner, via pokeInbound, floored by pokeFloor — the exact mechanism
// backend_waxum.go's statusPump already uses for its own poke, reusing
// pokeMinIntervalDivisor rather than inventing a second constant for the
// same idea), walking every connected session on each tick.
//
// mh is resolved ONCE, not per tick: the backend is fixed for a Manager's
// whole lifetime (NewManager sets m.backend once, never reassigned — see
// manager_capabilities.go's package doc comment), so if it doesn't
// implement MessageHistory now it never will. ensureInboundPump's own
// caller (waxumBackend.Start) only exists on a backend that DOES, so this
// is defensive, not expected to fire in production.
func (m *Manager) inboundPump(ctx context.Context) {
	mh, err := m.MessageHistory()
	if err != nil {
		return
	}
	cache := newLIDCache()

	interval := m.cfg.InboundPollInterval
	if interval <= 0 {
		// Defensive only — internal/config.loadInbound already rejects a
		// non-positive WSM_INBOUND_POLL_INTERVAL before this Manager is
		// ever constructed. A zero interval here would otherwise spin the
		// loop below in a tight busy-loop.
		interval = 60 * time.Second
	}
	var pokeFloor time.Duration
	if interval > 0 {
		pokeFloor = interval / pokeMinIntervalDivisor
	}

	var lastTick time.Time
	for {
		for _, snap := range m.Snapshots() {
			if !snap.Connected {
				continue
			}
			m.processSessionInbound(ctx, mh, cache, snap.ID)
		}
		lastTick = time.Now()

		select {
		case <-ctx.Done():
			return
		case <-m.inboundPoke:
			// An SSE "message" event nudged this pump — react sooner than
			// `interval`, but never sooner than pokeFloor since lastTick.
			// See statusPump's own poke-handling case for why a floor is
			// required, not optional: without one, a burst of "message"
			// events could compress this pump's effective cadence to
			// nearly zero, hammering waxum (and this process's own share
			// of its small per-IP rate-limit bucket) for no benefit —
			// nothing about a poke changes what a tick actually fetches.
			if since := time.Since(lastTick); since < pokeFloor {
				select {
				case <-ctx.Done():
					return
				case <-time.After(pokeFloor - since):
				}
			}
		case <-time.After(interval):
		}
	}
}

// processSessionInbound walks one session's newest waxum message history,
// newest first, following next_cursor only while every row on the current
// page was already new to us (inboundLastSeen's high-water mark) — see
// inboundMaxPages' doc comment for the bound on how far that can go in one
// tick. A page fetch failure is swallowed here: ErrNotConnected (waxum's
// 503 "not connected", OR a 404 for a session waxum no longer knows about —
// mapErr folds both onto the same sentinel) is treated as the NORMAL,
// expected outcome the brief calls out explicitly and is not logged at
// all; anything else is logged (session id only, never message content)
// and the tick moves on to the next session.
func (m *Manager) processSessionInbound(ctx context.Context, mh MessageHistory, cache *lidCache, sessionID string) {
	prevHighAny, _ := m.inboundLastSeen.Load(sessionID)
	prevHigh, _ := prevHighAny.(int64)
	newHigh := prevHigh

	var after *int64
	for page := 0; page < inboundMaxPages; page++ {
		result, err := mh.RecentMessages(ctx, sessionID, after, inboundPageSize)
		if err != nil {
			if !errors.Is(err, ErrNotConnected) {
				slog.Warn("wa: inbound pump: fetching messages failed", "session", sessionID, "err", err)
			}
			break
		}

		allNew := true
		for _, msg := range result.Messages {
			if msg.ID <= prevHigh {
				allNew = false
				continue // already processed on a previous tick
			}
			if msg.ID > newHigh {
				newHigh = msg.ID
			}
			m.handleInboundMessage(ctx, mh, cache, sessionID, msg)
		}

		if !allNew || result.NextCursor == nil {
			break
		}
		after = result.NextCursor
	}

	if newHigh > prevHigh {
		m.inboundLastSeen.Store(sessionID, newHigh)
	}
}

// handleInboundMessage applies the four drop rules the brief calls out —
// each one would be a BUG downstream if stored, not mere noise, so each
// gets its own check rather than one combined condition that would be
// harder to attribute a dropped row to:
//
//   - direction != "in": our own sends, including from the paired
//     account's OTHER devices (waxum maps is_from_me to "out" for both).
//     Storing one would let a consumer's reply-matcher read a question as
//     its own answer.
//   - chat_jid == "status@broadcast": not addressed to us at all.
//   - chat_jid ends in "@g.us": a group has no single sender to attribute
//     a stamp to, and no question-and-answer semantics — every question
//     this gateway's product asks is asked of one person.
//   - an empty/whitespace body: a sticker, image, or reaction. Nothing to
//     parse, and it would only grow a table nobody reads.
//
// Everything past that point resolves the sender to a phone (best-effort —
// see resolveSenderPhone), stamps against a recent outbound send in the
// window if one exists, and inserts — via m.persistCtx, not ctx, so a
// write already in flight at shutdown still lands (the same reasoning
// every other repo write in this package follows).
func (m *Manager) handleInboundMessage(ctx context.Context, mh MessageHistory, cache *lidCache, sessionID string, msg InboundMessage) {
	if msg.Direction != "in" {
		return
	}
	if msg.ChatJID == "status@broadcast" {
		return
	}
	if strings.HasSuffix(msg.ChatJID, "@g.us") {
		return
	}
	if msg.Body == nil || strings.TrimSpace(*msg.Body) == "" {
		return
	}

	phone, isLID := m.resolveSenderPhone(ctx, mh, cache, sessionID, msg.SenderJID)

	row := &inbound.Message{
		ID:         msg.MessageID,
		SessionID:  sessionID,
		FromPhone:  phone,
		FromJID:    msg.SenderJID,
		Body:       *msg.Body,
		ReceivedAt: msg.Timestamp,
	}
	if isLID {
		// Kept even when phone resolved successfully, matching
		// migrations/0006_inbound_messages.sql's own from_lid comment: a
		// resolution bug is diagnosable from the row itself later, not
		// just from logs at the moment of arrival — and this pump's logs
		// never carry the phone or LID anyway (see this file's package
		// doc comment).
		row.FromLID = msg.SenderJID
	}

	// Stamp: skipped entirely when phone is empty — Repository.LatestOutboundRefs
	// already refuses to query on an empty phone for the same reason (see
	// its own doc comment), but checking here too avoids a wasted query on
	// the common "sender never resolved" case.
	if phone != "" {
		refs, found, err := m.inbound.LatestOutboundRefs(ctx, phone,
			msg.Timestamp.Add(-m.cfg.InboundStampWindow), msg.Timestamp)
		switch {
		case err != nil:
			slog.Warn("wa: inbound pump: stamp lookup failed", "session", sessionID, "err", err)
		case found:
			row.ClientRef = refs.ClientRef
			row.TenantRef = refs.TenantRef
			row.RepliesToMessageID = refs.MessageID
		}
	}

	if err := m.inbound.Insert(m.persistCtx, row); err != nil {
		slog.Warn("wa: inbound pump: insert failed", "session", sessionID, "err", err)
	}
}

// splitSenderJID splits a waxum sender_jid ("1234:19@lid" or
// "6281234567890@s.whatsapp.net") into its user part (device suffix
// stripped) and server. A malformed value with no "@" is returned with the
// whole string as user and an empty server — resolveSenderPhone's
// server != lidServer branch then runs NormalizePhone on it, which will
// itself fail closed (best-effort: an unparseable sender simply resolves
// to no phone, not a panic or a propagated error).
func splitSenderJID(jid string) (user, server string) {
	full, srv, ok := strings.Cut(jid, "@")
	if !ok {
		return jid, ""
	}
	u, _, _ := strings.Cut(full, ":")
	return u, srv
}

// resolveSenderPhone is THE trap this whole pump exists to get right — see
// migrations/0006_inbound_messages.sql's header and this package's
// capabilities.go doc comment: on every observed live waxum deployment,
// 100% of inbound sender_jid values are "@lid", not a phone JID, and a LID
// is NOT a phone number — running wa.NormalizePhone on a LID's digits
// yields a value that can never equal any wa_messages.to_phone, silently
// breaking the stamp for every single inbound row if this branch were
// missed.
//
//   - server != "lid" (the "s.whatsapp.net" case — never actually observed
//     live per the brief, but the documented fallback): the user part IS
//     already a phone number, normalized directly via wa.NormalizePhone —
//     no waxum call, no cache entry.
//   - server == "lid": resolved through MessageHistory.ResolvePhone
//     (waxum's GET .../contacts/{jid}/lid), cached by the STRIPPED jid
//     (device suffix removed) so N messages from the same sender across a
//     tick, or across many ticks, cost at most one waxum call — see
//     lidCache's own doc comment for how long a result (positive OR
//     negative) is trusted. isLID is true whenever server == "lid",
//     REGARDLESS of whether resolution succeeded — callers use it to set
//     Message.FromLID even on a resolution failure (see
//     handleInboundMessage), which is the difference between an
//     unresolvable sender being diagnosable later and it silently
//     vanishing into an empty from_phone with no trace of why.
func (m *Manager) resolveSenderPhone(ctx context.Context, mh MessageHistory, cache *lidCache, sessionID, senderJID string) (phone string, isLID bool) {
	user, server := splitSenderJID(senderJID)
	if server != lidServer {
		p, err := NormalizePhone(user)
		if err != nil {
			return "", false
		}
		return p, false
	}

	strippedJID := user + "@" + server
	if cached, ok := cache.get(strippedJID); ok {
		return cached, true
	}

	resolved, err := mh.ResolvePhone(ctx, sessionID, strippedJID)
	if err != nil {
		// Covers both ErrNotFound (waxum has no mapping at all — the
		// brief's documented 404 case) and any other failure (a transient
		// waxum error, a rate limit exhausted after do()'s own retries):
		// either way, this sender is not resolvable RIGHT NOW, and caching
		// that briefly is what keeps a busy unmappable contact from
		// re-hitting waxum on every single tick — see lidCache's doc
		// comment for the TTL that lets a genuinely transient failure
		// self-heal instead of being cached forever.
		cache.setNegative(strippedJID)
		return "", true
	}
	p, err := NormalizePhone(resolved)
	if err != nil {
		cache.setNegative(strippedJID)
		return "", true
	}
	cache.set(strippedJID, p)
	return p, true
}

// lidCacheMaxEntries/lidNegativeCacheTTL bound lidCache — see that type's
// doc comment.
const (
	lidCacheMaxEntries  = 2000
	lidNegativeCacheTTL = 10 * time.Minute
)

// lidCacheEntry is one lidCache row: phone is empty (and negative true)
// for a sender known NOT to resolve — a real resolution to "" cannot
// happen, since resolveSenderPhone's own NormalizePhone check rejects an
// empty/malformed number before ever calling cache.set.
type lidCacheEntry struct {
	phone    string
	negative bool
	at       time.Time
}

// lidCache is a bounded, in-memory, per-Manager cache of @lid -> resolved
// phone (or "known unresolvable"), existing so a busy sender costs ONE
// waxum ResolveLID call, not one per inbound message (per the brief:
// "one call per new sender, not per message"). A positive entry is trusted
// for the process's whole lifetime — a LID's phone mapping is not expected
// to change once waxum has learned it — while a negative entry expires
// after lidNegativeCacheTTL, so a contact waxum genuinely cannot map YET
// (rather than never) is retried periodically instead of being
// permanently blacklisted by one unlucky first attempt.
//
// Bounded, not precise LRU: evictIfFullLocked drops every entry at once
// when the map reaches lidCacheMaxEntries rather than tracking recency.
// The cost of that is small and well-understood — a resolved sender simply
// re-resolves once more, on its next message, after an eviction — and
// accepting it avoids carrying a full LRU implementation for a cache this
// size exists to bound at all, not to optimize precisely.
type lidCache struct {
	mu      sync.Mutex
	entries map[string]lidCacheEntry
}

func newLIDCache() *lidCache {
	return &lidCache{entries: make(map[string]lidCacheEntry)}
}

// get returns the cached phone (empty for a cached-negative entry) and
// whether jid has a live cache entry at all. An expired negative entry is
// evicted here and reported as a miss, so the caller resolves it fresh.
func (c *lidCache) get(jid string) (phone string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, found := c.entries[jid]
	if !found {
		return "", false
	}
	if e.negative && time.Since(e.at) > lidNegativeCacheTTL {
		delete(c.entries, jid)
		return "", false
	}
	return e.phone, true
}

func (c *lidCache) set(jid, phone string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictIfFullLocked()
	c.entries[jid] = lidCacheEntry{phone: phone, at: time.Now()}
}

func (c *lidCache) setNegative(jid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictIfFullLocked()
	c.entries[jid] = lidCacheEntry{negative: true, at: time.Now()}
}

func (c *lidCache) evictIfFullLocked() {
	if len(c.entries) < lidCacheMaxEntries {
		return
	}
	c.entries = make(map[string]lidCacheEntry)
}
