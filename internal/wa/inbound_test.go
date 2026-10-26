package wa

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/inbound"
)

// fakeMessageHistoryBackend is a fakeBackend (backend_test.go) that also
// implements MessageHistory (capabilities.go). Deliberately does NOT
// implement the optional Start(*Manager) lifecycle hook (manager.go's
// `backend.(interface{ Start(*Manager) })` branch, the one waxumBackend
// uses to call m.ensureInboundPump()): every test in this file except
// TestInboundPumpExitsWhenTheManagerCloses calls processSessionInbound
// directly, against its OWN freshly-built lidCache — if NewManager also
// auto-started the real background pump (which builds and owns a
// DIFFERENT lidCache internally), that goroutine would race the test's own
// direct call against the SAME fixture data with a DIFFERENT cache
// instance, non-deterministically doubling ResolvePhone's call count
// depending on scheduling. TestInboundPumpExitsWhenTheManagerCloses starts
// the pump explicitly, via mgr.ensureInboundPump(), instead.
//
// Live always reports true: every test in this file wants its registered
// session treated as Connected without wiring a real statusPump.
type fakeMessageHistoryBackend struct {
	*fakeBackend

	mu                sync.Mutex
	messagesBySession map[string][]InboundMessage
	recentErr         error
	recentCalls       atomic.Int32

	resolvePhone map[string]string
	resolveErr   map[string]error
	resolveCalls atomic.Int32
}

func newFakeMessageHistoryBackend() *fakeMessageHistoryBackend {
	return &fakeMessageHistoryBackend{
		fakeBackend:       &fakeBackend{name: "fake-message-history"},
		messagesBySession: map[string][]InboundMessage{},
		resolvePhone:      map[string]string{},
		resolveErr:        map[string]error{},
	}
}

func (f *fakeMessageHistoryBackend) Live(*ManagedSession) bool { return true }

func (f *fakeMessageHistoryBackend) RecentMessages(ctx context.Context, sessionID string, after *int64, limit int) (MessagePage, error) {
	f.recentCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recentErr != nil {
		return MessagePage{}, f.recentErr
	}
	return MessagePage{Messages: f.messagesBySession[sessionID]}, nil
}

func (f *fakeMessageHistoryBackend) ResolvePhone(ctx context.Context, sessionID, jid string) (string, error) {
	f.resolveCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.resolveErr[jid]; ok {
		return "", err
	}
	if phone, ok := f.resolvePhone[jid]; ok {
		return phone, nil
	}
	return "", ErrNotFound
}

// fakeOutboundRow is one row fakeInboundRepo.LatestOutboundRefs can match
// against — the fake's own stand-in for a wa_messages row, since these
// tests live in package wa and must not reach into internal/domain/message.
type fakeOutboundRow struct {
	phone  string
	refs   inbound.Refs
	sentAt time.Time
}

// fakeInboundRepo implements inbound.Repository entirely in memory, so
// these tests exercise inbound.go's pump logic without a real SQLite/MySQL
// pool. LatestOutboundRefs applies the SAME notBefore/notAfter window
// semantics the real SQLRepository does (internal/domain/inbound/repository.go),
// which is what lets TestAStampedRowsWindowIsAnchoredOnTheArrivalsOwnTimestamp
// below actually prove anchoring rather than assuming it.
type fakeInboundRepo struct {
	mu       sync.Mutex
	inserted []*inbound.Message
	outbound []fakeOutboundRow
}

func (r *fakeInboundRepo) Insert(ctx context.Context, m *inbound.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *m
	r.inserted = append(r.inserted, &cp)
	return nil
}

func (r *fakeInboundRepo) Since(ctx context.Context, t time.Time, limit int) ([]inbound.Message, error) {
	return nil, nil // unused by these tests — GET /api/v1/inbound is Stage 3
}

func (r *fakeInboundRepo) LatestOutboundRefs(ctx context.Context, phone string, notBefore, notAfter time.Time) (inbound.Refs, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if phone == "" {
		return inbound.Refs{}, false, nil
	}
	var best *fakeOutboundRow
	for i := range r.outbound {
		row := &r.outbound[i]
		if row.phone != phone {
			continue
		}
		if row.sentAt.Before(notBefore) || row.sentAt.After(notAfter) {
			continue
		}
		if best == nil || row.sentAt.After(best.sentAt) {
			best = row
		}
	}
	if best == nil {
		return inbound.Refs{}, false, nil
	}
	return best.refs, true, nil
}

func (r *fakeInboundRepo) insertedRows() []*inbound.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*inbound.Message, len(r.inserted))
	copy(out, r.inserted)
	return out
}

// newInboundTestManager wires a Manager whose MessageHistory is backend and
// whose inbound store is repo, with a connected session registered under
// sessionID — the shape every drop-rule/resolution/stamp test below needs.
func newInboundTestManager(t *testing.T, backend *fakeMessageHistoryBackend, repo inbound.Repository, sessionID string) *Manager {
	t.Helper()
	mgr := NewManager(context.Background(), backend, newStubSessions(), newStubMsgs(), repo,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2,
			InboundEnabled: true, InboundPollInterval: time.Hour, InboundStampWindow: 24 * time.Hour})
	// Built directly (mirroring testSession, sender_test.go) rather than
	// via testSession itself, which always mints a fresh random ID — every
	// fixture below needs a KNOWN id to key messagesBySession/resolvePhone
	// by. Live()=true (fakeMessageHistoryBackend) + StateConnected is what
	// Snapshot.Connected requires (session.go).
	s := &ManagedSession{ID: sessionID, name: "t", Phone: "628120000",
		mgr: mgr, state: StateConnected, events: NewRing(ringSize)}
	registerSession(mgr, s)
	return mgr
}

func body(s string) *string { return &s }

// TestOwnSendsGroupChatsAndStatusBroadcastsAreDroppedBeforeTheDatabase
// guards the three drop rules the brief calls BUGS, not noise, if missed:
// an echoed-back own send (direction "out", including from the paired
// account's OTHER devices), a status broadcast, and a group chat. Each
// would corrupt the feature in a different way if stored — see
// handleInboundMessage's doc comment — so this asserts NO row reaches the
// repository for any of the three.
func TestOwnSendsGroupChatsAndStatusBroadcastsAreDroppedBeforeTheDatabase(t *testing.T) {
	const sessionID = "sess-drop"
	backend := newFakeMessageHistoryBackend()
	backend.messagesBySession[sessionID] = []InboundMessage{
		{ID: 1, MessageID: "m1", ChatJID: "628120000001@s.whatsapp.net", SenderJID: "628120000001@s.whatsapp.net",
			Direction: "out", Body: body("our own message, echoed back"), Timestamp: time.Now().UTC()},
		{ID: 2, MessageID: "m2", ChatJID: "status@broadcast", SenderJID: "628120000002@s.whatsapp.net",
			Direction: "in", Body: body("status update"), Timestamp: time.Now().UTC()},
		{ID: 3, MessageID: "m3", ChatJID: "120363000000000000@g.us", SenderJID: "628120000003@s.whatsapp.net",
			Direction: "in", Body: body("group reply"), Timestamp: time.Now().UTC()},
	}
	repo := &fakeInboundRepo{}
	mgr := newInboundTestManager(t, backend, repo, sessionID)

	mh, err := mgr.MessageHistory()
	if err != nil {
		t.Fatalf("MessageHistory: %v", err)
	}
	mgr.processSessionInbound(context.Background(), mh, newLIDCache(), sessionID)

	if got := repo.insertedRows(); len(got) != 0 {
		t.Fatalf("inserted %d rows, want 0 (direction=out, status@broadcast, and @g.us must all be dropped): %+v", len(got), got)
	}
}

// TestAnEmptyOrWhitespaceBodyIsDropped guards the fourth drop rule: a
// sticker, image, or reaction carries a null/empty body, and storing it
// would only grow a table nobody reads (handleInboundMessage's doc
// comment).
func TestAnEmptyOrWhitespaceBodyIsDropped(t *testing.T) {
	const sessionID = "sess-empty-body"
	backend := newFakeMessageHistoryBackend()
	backend.messagesBySession[sessionID] = []InboundMessage{
		{ID: 1, MessageID: "m1", ChatJID: "628120000001@s.whatsapp.net", SenderJID: "628120000001@s.whatsapp.net",
			Direction: "in", Body: nil, Timestamp: time.Now().UTC()},
		{ID: 2, MessageID: "m2", ChatJID: "628120000001@s.whatsapp.net", SenderJID: "628120000001@s.whatsapp.net",
			Direction: "in", Body: body("   "), Timestamp: time.Now().UTC()},
	}
	repo := &fakeInboundRepo{}
	mgr := newInboundTestManager(t, backend, repo, sessionID)

	mh, err := mgr.MessageHistory()
	if err != nil {
		t.Fatalf("MessageHistory: %v", err)
	}
	mgr.processSessionInbound(context.Background(), mh, newLIDCache(), sessionID)

	if got := repo.insertedRows(); len(got) != 0 {
		t.Fatalf("inserted %d rows, want 0 (a null and a whitespace-only body must both be dropped): %+v", len(got), got)
	}
}

// TestALIDSenderWithADeviceSuffixIsResolvedToItsMSISDNNotTheLIDDigits is
// THE trap test: waxum's sender_jid is a @lid, not a phone number, on every
// observed live row (migrations/0006_inbound_messages.sql's header) —
// running wa.NormalizePhone directly on a LID's digits (here, "999888") is
// exactly the bug that would silently ship: FromPhone would end up holding
// digits that can never equal any wa_messages.to_phone, breaking the stamp
// for every single inbound row. The stored FromPhone must be the RESOLVED
// MSISDN ("6281200009999"), not "999888", and the device suffix (":19")
// must be stripped before the resolve call.
func TestALIDSenderWithADeviceSuffixIsResolvedToItsMSISDNNotTheLIDDigits(t *testing.T) {
	const sessionID = "sess-lid"
	backend := newFakeMessageHistoryBackend()
	backend.messagesBySession[sessionID] = []InboundMessage{
		{ID: 1, MessageID: "m1", ChatJID: "999888@lid", SenderJID: "999888:19@lid",
			Direction: "in", Body: body("sudah"), Timestamp: time.Now().UTC()},
	}
	// Keyed by the STRIPPED jid (no ":19") — resolveSenderPhone must strip
	// the device suffix before calling ResolvePhone, or this lookup misses
	// entirely and the message falls through to the unresolved case.
	backend.resolvePhone["999888@lid"] = "6281200009999"
	repo := &fakeInboundRepo{}
	mgr := newInboundTestManager(t, backend, repo, sessionID)

	mh, err := mgr.MessageHistory()
	if err != nil {
		t.Fatalf("MessageHistory: %v", err)
	}
	mgr.processSessionInbound(context.Background(), mh, newLIDCache(), sessionID)

	got := repo.insertedRows()
	if len(got) != 1 {
		t.Fatalf("inserted %d rows, want 1", len(got))
	}
	if got[0].FromPhone != "6281200009999" {
		t.Errorf("FromPhone = %q, want the RESOLVED MSISDN %q, not the LID's own digits", got[0].FromPhone, "6281200009999")
	}
	if got[0].FromLID != "999888:19@lid" {
		t.Errorf("FromLID = %q, want the raw sender_jid %q kept for diagnosis", got[0].FromLID, "999888:19@lid")
	}
}

// TestAnUnresolvableLIDStoresTheRowWithFromLIDSetAndFromPhoneEmptyAndNoStamp
// guards the "diagnosable, not silently gone" requirement: when waxum has
// no known mapping for a @lid sender (ErrNotFound), the row must still be
// stored — with FromLID set to the raw sender identity and FromPhone left
// empty — rather than dropped, and it must NOT be stamped (an empty phone
// can never match anyone's order; see Repository.LatestOutboundRefs' own
// doc comment on this exact security property).
func TestAnUnresolvableLIDStoresTheRowWithFromLIDSetAndFromPhoneEmptyAndNoStamp(t *testing.T) {
	const sessionID = "sess-unresolvable-lid"
	backend := newFakeMessageHistoryBackend()
	backend.messagesBySession[sessionID] = []InboundMessage{
		{ID: 1, MessageID: "m1", ChatJID: "777666@lid", SenderJID: "777666@lid",
			Direction: "in", Body: body("halo"), Timestamp: time.Now().UTC()},
	}
	backend.resolveErr["777666@lid"] = ErrNotFound
	repo := &fakeInboundRepo{
		// Even a wide-open outbound row must not stamp an unresolved
		// sender — this is the "must never guess" half of the security
		// property, exercised alongside the phone-only test elsewhere.
		outbound: []fakeOutboundRow{{phone: "", refs: inbound.Refs{ClientRef: "should-never-match"}, sentAt: time.Now().UTC()}},
	}
	mgr := newInboundTestManager(t, backend, repo, sessionID)

	mh, err := mgr.MessageHistory()
	if err != nil {
		t.Fatalf("MessageHistory: %v", err)
	}
	mgr.processSessionInbound(context.Background(), mh, newLIDCache(), sessionID)

	got := repo.insertedRows()
	if len(got) != 1 {
		t.Fatalf("inserted %d rows, want 1 (an unresolvable LID must still be stored)", len(got))
	}
	if got[0].FromPhone != "" {
		t.Errorf("FromPhone = %q, want empty for an unresolvable LID", got[0].FromPhone)
	}
	if got[0].FromLID != "777666@lid" {
		t.Errorf("FromLID = %q, want the raw sender identity %q", got[0].FromLID, "777666@lid")
	}
	if got[0].ClientRef != "" || got[0].TenantRef != "" || got[0].RepliesToMessageID != "" {
		t.Errorf("row was stamped (%+v) despite an unresolvable sender — must stay un-stamped", got[0])
	}
}

// TestTheLIDResolutionCacheMeansNMessagesFromOneSenderCauseOneResolveCall
// guards the cost the brief explicitly calls out: "one call per new
// sender, not per message". Three messages from the SAME @lid sender in
// one tick must resolve exactly once.
func TestTheLIDResolutionCacheMeansNMessagesFromOneSenderCauseOneResolveCall(t *testing.T) {
	const sessionID = "sess-cache"
	backend := newFakeMessageHistoryBackend()
	sender := "555444@lid"
	backend.resolvePhone[sender] = "6281200005555"
	var msgs []InboundMessage
	for i := 1; i <= 3; i++ {
		msgs = append(msgs, InboundMessage{
			ID: int64(i), MessageID: "m" + string(rune('0'+i)),
			ChatJID: sender, SenderJID: sender,
			Direction: "in", Body: body("halo lagi"), Timestamp: time.Now().UTC(),
		})
	}
	backend.messagesBySession[sessionID] = msgs
	repo := &fakeInboundRepo{}
	mgr := newInboundTestManager(t, backend, repo, sessionID)

	mh, err := mgr.MessageHistory()
	if err != nil {
		t.Fatalf("MessageHistory: %v", err)
	}
	mgr.processSessionInbound(context.Background(), mh, newLIDCache(), sessionID)

	if got := repo.insertedRows(); len(got) != 3 {
		t.Fatalf("inserted %d rows, want 3", len(got))
	}
	if got := backend.resolveCalls.Load(); got != 1 {
		t.Errorf("ResolvePhone called %d times for 3 messages from one sender, want exactly 1", got)
	}
}

// TestAStampedRowsWindowIsAnchoredOnTheArrivalsOwnTimestamp guards the
// departure from the reference implementation Repository.LatestOutboundRefs'
// own doc comment describes: this pump POLLS a backlog, so the window must
// be anchored on the ARRIVAL's own timestamp, not time.Now() — an arrival
// processed long after it was actually sent must still match an outbound
// row that was recent RELATIVE TO THE ARRIVAL, and an outbound row that
// would be "recent" only relative to wall-clock-now must NOT match.
func TestAStampedRowsWindowIsAnchoredOnTheArrivalsOwnTimestamp(t *testing.T) {
	const sessionID = "sess-stamp-window"
	const phone = "6281200007777"
	// The arrival itself is old — days ago, as if this tick is draining a
	// backlog well after the fact. If the pump anchored on time.Now()
	// instead of the arrival's own Timestamp, NEITHER outbound row below
	// would be "recent" relative to now, and the stamp would wrongly fail.
	arrivedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	backend := newFakeMessageHistoryBackend()
	backend.messagesBySession[sessionID] = []InboundMessage{
		{ID: 1, MessageID: "m1", ChatJID: phone + "@s.whatsapp.net", SenderJID: phone + "@s.whatsapp.net",
			Direction: "in", Body: body("sudah diterima"), Timestamp: arrivedAt},
	}
	repo := &fakeInboundRepo{
		outbound: []fakeOutboundRow{
			// 2 hours before the arrival — inside the 24h window ANCHORED
			// ON THE ARRIVAL, but ~3.5 weeks before time.Now() (this test
			// runs in 2026), which would be nowhere near "recent" if the
			// pump wrongly anchored on wall-clock-now instead.
			{phone: phone, refs: inbound.Refs{ClientRef: "order-abc", TenantRef: "tenant-1", MessageID: "wamid-abc"},
				sentAt: arrivedAt.Add(-2 * time.Hour)},
		},
	}
	mgr := newInboundTestManager(t, backend, repo, sessionID)

	mh, err := mgr.MessageHistory()
	if err != nil {
		t.Fatalf("MessageHistory: %v", err)
	}
	mgr.processSessionInbound(context.Background(), mh, newLIDCache(), sessionID)

	got := repo.insertedRows()
	if len(got) != 1 {
		t.Fatalf("inserted %d rows, want 1", len(got))
	}
	if got[0].ClientRef != "order-abc" || got[0].TenantRef != "tenant-1" || got[0].RepliesToMessageID != "wamid-abc" {
		t.Errorf("row not stamped from the arrival-anchored window: %+v", got[0])
	}
	if !got[0].ReceivedAt.Equal(arrivedAt) {
		t.Errorf("ReceivedAt = %v, want the envelope's own timestamp %v unchanged", got[0].ReceivedAt, arrivedAt)
	}
}

// TestARowWithNoRecentOutboundWithinTheWindowIsNotStamped is the negative
// half of the window test above: an outbound row that exists but falls
// OUTSIDE the window (anchored on the arrival) must leave the row
// un-stamped — NULL/empty, never a guess — matching the brief's "tier
// stamp is the security property" section.
func TestARowWithNoRecentOutboundWithinTheWindowIsNotStamped(t *testing.T) {
	const sessionID = "sess-no-stamp"
	const phone = "6281200008888"
	arrivedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	backend := newFakeMessageHistoryBackend()
	backend.messagesBySession[sessionID] = []InboundMessage{
		{ID: 1, MessageID: "m1", ChatJID: phone + "@s.whatsapp.net", SenderJID: phone + "@s.whatsapp.net",
			Direction: "in", Body: body("halo"), Timestamp: arrivedAt},
	}
	repo := &fakeInboundRepo{
		outbound: []fakeOutboundRow{
			// 30 hours before the arrival — OUTSIDE the 24h window.
			{phone: phone, refs: inbound.Refs{ClientRef: "order-old"}, sentAt: arrivedAt.Add(-30 * time.Hour)},
		},
	}
	mgr := newInboundTestManager(t, backend, repo, sessionID)

	mh, err := mgr.MessageHistory()
	if err != nil {
		t.Fatalf("MessageHistory: %v", err)
	}
	mgr.processSessionInbound(context.Background(), mh, newLIDCache(), sessionID)

	got := repo.insertedRows()
	if len(got) != 1 {
		t.Fatalf("inserted %d rows, want 1", len(got))
	}
	if got[0].ClientRef != "" || got[0].TenantRef != "" || got[0].RepliesToMessageID != "" {
		t.Errorf("row was stamped from an outbound send OUTSIDE the window: %+v", got[0])
	}
}

// TestInboundPumpExitsWhenTheManagerCloses mirrors
// TestManagerCloseTerminatesTheEventPump's own framing exactly (that
// test's doc comment calls itself "the single most important test in this
// phase" for the SSE event pump; this is the same hazard for the inbound
// pump): an untracked or wrongly-keyed pump would hang Manager.Close
// indefinitely, and CLAUDE.md documents exactly what a SIGKILLed-mid-teardown
// waxum session costs. InboundPollInterval is set to an hour specifically
// so this test cannot pass by accident (the pump ticking on its own before
// the assertion) — only Close's supCtx cancellation can end it in time.
func TestInboundPumpExitsWhenTheManagerCloses(t *testing.T) {
	backend := newFakeMessageHistoryBackend()
	repo := &fakeInboundRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := NewManager(ctx, backend, newStubSessions(), newStubMsgs(), repo,
		Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2,
			InboundEnabled: true, InboundPollInterval: time.Hour, InboundStampWindow: 24 * time.Hour})
	// Started explicitly rather than via backend.(interface{ Start(*Manager) })
	// — fakeMessageHistoryBackend deliberately doesn't implement that hook
	// (see its own doc comment) — this test only needs the pump goroutine
	// itself running and wg-tracked, not the production wiring path that
	// starts it (a one-line addition to waxumBackend.Start, unrelated to
	// this goroutine-lifecycle claim).
	mgr.ensureInboundPump()

	closeDone := make(chan struct{})
	go func() {
		mgr.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Manager.Close did not return promptly with the inbound pump waiting on a 1h poll interval " +
			"(the pump must be wg-tracked via m.go1 and keyed to m.supCtx, mirroring TestManagerCloseTerminatesTheEventPump)")
	}
}
