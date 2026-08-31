package inbound

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
)

// newTestRepo mirrors internal/domain/call's helper of the same name: a
// fresh SQLite pool, schema created via EnsureSchema, one seeded session.
// The raw pool is also returned so tests can seed a wa_messages row directly
// (the "outbound side" LatestOutboundRefs reads) without importing
// internal/domain/message for a single INSERT.
func newTestRepo(t *testing.T) (*SQLRepository, *sql.DB, context.Context) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	ctx := context.Background()
	if err := db.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_sessions (id, name, phone, created_at, updated_at)
		 VALUES ('sess-1', 'sess-1', '628000', '2026-07-01T00:00:00Z', '2026-07-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}
	return NewRepository(pool), pool, ctx
}

var idSeq int

// nextID returns a fresh, unique id per call, so tests never collide on
// wa_inbound_messages' or wa_messages' primary key by accident.
func nextID(prefix string) string {
	idSeq++
	return fmt.Sprintf("%s-%04d", prefix, idSeq)
}

// seedOutbound inserts a wa_messages row directly — bypassing
// internal/domain/message entirely, since these tests only need a row
// LatestOutboundRefs can read, not that package's own Insert behaviour.
func seedOutbound(t *testing.T, ctx context.Context, pool *sql.DB, id, toPhone, clientRef, tenantRef string, createdAt time.Time) {
	t.Helper()
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO wa_messages (id, session_id, to_phone, body, client_ref, tenant_ref, created_at)
		 VALUES (?, 'sess-1', ?, 'hi', ?, ?, ?)`,
		id, toPhone, clientRef, tenantRef, createdAt.UTC().Format(timeLayout)); err != nil {
		t.Fatalf("seedOutbound: %v", err)
	}
}

// TestSinceReturnsArrivalsOldestFirstEvenWhenTheLaterOneWasInsertedFirst
// proves the brief's non-cosmetic ordering rule: the consumer resolves a
// reply against the open question it answers, so a newest-first (or
// insertion-order) result would let a later message close a question an
// earlier one already answered.
func TestSinceReturnsArrivalsOldestFirstEvenWhenTheLaterOneWasInsertedFirst(t *testing.T) {
	r, _, ctx := newTestRepo(t)

	older := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)

	// Insert the NEWER message first, to prove the returned order comes
	// from received_at, not insertion order.
	if err := r.Insert(ctx, &Message{
		ID: nextID("msg"), SessionID: "sess-1", FromPhone: "6281111111",
		FromJID: "6281111111@s.whatsapp.net", Body: "second", ReceivedAt: newer,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Insert(ctx, &Message{
		ID: nextID("msg"), SessionID: "sess-1", FromPhone: "6281111111",
		FromJID: "6281111111@s.whatsapp.net", Body: "first", ReceivedAt: older,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := r.Since(ctx, older.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Since returned %d rows, want 2", len(got))
	}
	if got[0].Body != "first" || got[1].Body != "second" {
		t.Errorf("Since order = [%q, %q], want [\"first\", \"second\"] (oldest first)",
			got[0].Body, got[1].Body)
	}
}

// TestLatestOutboundRefsStampsAMatchingPhoneAndLeavesAnUnmatchedPhoneUnstamped
// is the security property from the brief, tested in BOTH directions: a
// phone with a recent outbound gets that row's refs, and a phone with no
// outbound in the window gets an empty, unstamped Refs — never a guess.
func TestLatestOutboundRefsStampsAMatchingPhoneAndLeavesAnUnmatchedPhoneUnstamped(t *testing.T) {
	r, pool, ctx := newTestRepo(t)

	sentAt := time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC)
	seedOutbound(t, ctx, pool, nextID("wam"), "6281111111", "client-abc", "tenant-xyz", sentAt)

	notBefore := sentAt.Add(-time.Hour)
	notAfter := sentAt.Add(time.Hour)

	// Direction 1: matching phone, inside the window — gets stamped.
	refs, found, err := r.LatestOutboundRefs(ctx, "6281111111", notBefore, notAfter)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("LatestOutboundRefs found=false, want true for a phone with a matching outbound row")
	}
	if refs.ClientRef != "client-abc" || refs.TenantRef != "tenant-xyz" {
		t.Errorf("refs = %+v, want ClientRef=client-abc TenantRef=tenant-xyz", refs)
	}

	// Direction 2: a DIFFERENT phone, no outbound at all — must NOT be
	// stamped with someone else's refs, or with a guess.
	refs2, found2, err := r.LatestOutboundRefs(ctx, "6282222222", notBefore, notAfter)
	if err != nil {
		t.Fatal(err)
	}
	if found2 {
		t.Fatalf("LatestOutboundRefs found=true for an unmatched phone, want false; got %+v", refs2)
	}
	if refs2 != (Refs{}) {
		t.Errorf("refs for unmatched phone = %+v, want zero value", refs2)
	}
}

// TestLatestOutboundRefsIgnoresAnOutboundRowOlderThanTheWindow proves the
// notBefore bound: a 24h-old send must not stamp a fresh arrival, or a
// customer replying out of the blue months later would be wrongly
// attributed to a stale order.
func TestLatestOutboundRefsIgnoresAnOutboundRowOlderThanTheWindow(t *testing.T) {
	r, pool, ctx := newTestRepo(t)

	arrivalTime := time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC)
	staleOutbound := arrivalTime.Add(-48 * time.Hour) // well outside a 24h window
	seedOutbound(t, ctx, pool, nextID("wam"), "6281111111", "client-abc", "tenant-xyz", staleOutbound)

	notBefore := arrivalTime.Add(-24 * time.Hour)
	notAfter := arrivalTime

	refs, found, err := r.LatestOutboundRefs(ctx, "6281111111", notBefore, notAfter)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("LatestOutboundRefs found=true for an outbound row outside the window, want false; got %+v", refs)
	}
}

// TestLatestOutboundRefsIgnoresAnOutboundRowSentAfterTheArrival proves the
// notAfter bound — the deliberate departure from the reference
// implementation's time.Now() anchor. This gateway polls a backlog, so an
// outbound sent AFTER the arrival being processed must not stamp it: that
// would attribute a customer's reply to an order that did not exist yet
// when they replied.
func TestLatestOutboundRefsIgnoresAnOutboundRowSentAfterTheArrival(t *testing.T) {
	r, pool, ctx := newTestRepo(t)

	arrivalTime := time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC)
	futureOutbound := arrivalTime.Add(2 * time.Hour) // sent AFTER the arrival
	seedOutbound(t, ctx, pool, nextID("wam"), "6281111111", "client-abc", "tenant-xyz", futureOutbound)

	notBefore := arrivalTime.Add(-24 * time.Hour)
	notAfter := arrivalTime

	refs, found, err := r.LatestOutboundRefs(ctx, "6281111111", notBefore, notAfter)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("LatestOutboundRefs found=true for an outbound row sent after the arrival, want false; got %+v", refs)
	}
}

// TestInsertingTheSameMessageIDTwiceInsertsOneRowNotTwo proves Insert's
// portable redelivery handling: the WhatsApp message id is the natural
// idempotency key, and the event stream that feeds this table is lossy by
// design, so a redelivered event must not duplicate the row.
func TestInsertingTheSameMessageIDTwiceInsertsOneRowNotTwo(t *testing.T) {
	r, pool, ctx := newTestRepo(t)

	id := nextID("msg")
	m := &Message{
		ID: id, SessionID: "sess-1", FromPhone: "6281111111",
		FromJID: "6281111111@s.whatsapp.net", Body: "sudah",
		ReceivedAt: time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC),
	}
	if err := r.Insert(ctx, m); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	// Redeliver the identical id.
	if err := r.Insert(ctx, m); err != nil {
		t.Fatalf("second Insert (redelivery) returned an error, want nil: %v", err)
	}

	var count int
	if err := pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wa_inbound_messages WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("wa_inbound_messages has %d rows for id %q, want 1 (redelivery must not duplicate)", count, id)
	}
}

// TestLatestOutboundRefsWithAnEmptyPhoneFindsNothingEvenWhenAWaMessagesRowHasAnEmptyToPhone
// proves the empty-phone guard: an unresolved LID (FromPhone == "") must
// never accidentally match a wa_messages row whose to_phone also happens to
// be empty — that would attribute an unidentifiable sender to someone else's
// order, exactly the failure the empty-phone short-circuit exists to
// prevent.
func TestLatestOutboundRefsWithAnEmptyPhoneFindsNothingEvenWhenAWaMessagesRowHasAnEmptyToPhone(t *testing.T) {
	r, pool, ctx := newTestRepo(t)

	sentAt := time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC)
	seedOutbound(t, ctx, pool, nextID("wam"), "", "client-abc", "tenant-xyz", sentAt)

	refs, found, err := r.LatestOutboundRefs(ctx, "", sentAt.Add(-time.Hour), sentAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("LatestOutboundRefs found=true for an empty phone, want false; got %+v", refs)
	}
	if refs != (Refs{}) {
		t.Errorf("refs for empty phone = %+v, want zero value", refs)
	}
}

// TestSinceLimitClampsToTheBriefsDefaultAndMaximum proves both halves of
// `if limit <= 0 || limit > 500 { limit = 200 }`:
//   - limit=0 must NOT become a literal SQL "LIMIT 0" (which returns zero
//     rows in both MySQL and SQLite) — it must be clamped up to 200, so a
//     caller that forgot to set it still gets a useful page.
//   - limit=9999 must be clamped DOWN to 200, not passed through as a
//     literal LIMIT 9999 — proven here by seeding 501 rows (more than the
//     500 cap and 200 default) and asserting exactly 200 come back.
func TestSinceLimitClampsToTheBriefsDefaultAndMaximum(t *testing.T) {
	r, _, ctx := newTestRepo(t)

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	const total = 501
	for i := 0; i < total; i++ {
		if err := r.Insert(ctx, &Message{
			ID: nextID("msg"), SessionID: "sess-1", FromPhone: "6281111111",
			FromJID:    "6281111111@s.whatsapp.net",
			Body:       "x",
			ReceivedAt: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	gotZero, err := r.Since(ctx, base.Add(-time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotZero) != 200 {
		t.Errorf("Since(limit=0) returned %d rows, want 200 (clamp-up default)", len(gotZero))
	}

	gotHuge, err := r.Since(ctx, base.Add(-time.Hour), 9999)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotHuge) != 200 {
		t.Errorf("Since(limit=9999) returned %d rows, want 200 (clamp-down cap), out of %d available", len(gotHuge), total)
	}
}

// wib is a fixed +07:00 zone (Asia/Jakarta's offset) built without tzdata, so
// these tests run identically on a machine with no zoneinfo installed.
var wib = time.FixedZone("WIB", 7*60*60)

// TestSinceMatchesACallerSuppliedTimeInANonUTCZone is the regression test for
// a trap specific to this schema: received_at/created_at are RFC3339 STRINGS
// and every comparison on them (Since's `received_at >= ?`, its ORDER BY,
// LatestOutboundRefs' window) is therefore a LEXICOGRAPHIC string compare, not
// an instant compare.
//
// The same instant renders as "2026-08-31T05:00:00Z" or
// "2026-08-31T12:00:00+07:00" depending only on the time.Time's location, and
// those two strings do not compare the way the instants do. A consumer sending
// `since` with a +07:00 offset (entirely legal RFC3339, and the local zone of
// every product this gateway serves) would silently miss rows.
//
// So every time.Time crossing this repository is normalized with .UTC() before
// Format. Reverting that makes this test fail: the stored row renders "05" and
// the query parameter renders "11", and "05..." < "11..." lexicographically, so
// the row is not returned at all.
func TestSinceMatchesACallerSuppliedTimeInANonUTCZone(t *testing.T) {
	repo, _, ctx := newTestRepo(t)

	arrival := time.Date(2026, 8, 31, 5, 0, 0, 0, time.UTC)
	id := nextID("wibmatch")
	if err := repo.Insert(ctx, &Message{
		ID: id, SessionID: "sess-1", FromPhone: "628123456789",
		FromJID: "628123456789@s.whatsapp.net", Body: "sudah",
		ReceivedAt: arrival,
	}); err != nil {
		t.Fatal(err)
	}

	// 04:00Z expressed as 11:00+07:00 — one hour BEFORE the arrival, so the
	// row must come back.
	since := time.Date(2026, 8, 31, 11, 0, 0, 0, wib)
	if !since.Before(arrival) {
		t.Fatalf("test fixture is wrong: since %s is not before arrival %s", since, arrival)
	}

	got, err := repo.Since(ctx, since, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("Since(%s) returned %d row(s), want exactly the arrival %q — "+
			"a +07:00 offset must select the same instants a Z offset does", since, len(got), id)
	}
}

// TestInsertStoresReceivedAtAsUTCWhateverZoneItArrivesIn is the write-side half
// of the trap above: a row stored with a "+07:00" suffix poisons every later
// string comparison against it, including ORDER BY, no matter how carefully the
// READ side normalizes.
func TestInsertStoresReceivedAtAsUTCWhateverZoneItArrivesIn(t *testing.T) {
	repo, pool, ctx := newTestRepo(t)

	id := nextID("wibstore")
	if err := repo.Insert(ctx, &Message{
		ID: id, SessionID: "sess-1", FromPhone: "628123456789",
		FromJID: "628123456789@s.whatsapp.net", Body: "sudah",
		ReceivedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, wib),
	}); err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := pool.QueryRowContext(ctx,
		`SELECT received_at FROM wa_inbound_messages WHERE id = ?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "2026-08-31T05:00:00Z" {
		t.Fatalf("received_at stored as %q, want %q — a non-UTC offset in the column "+
			"breaks every lexicographic comparison the schema relies on", stored, "2026-08-31T05:00:00Z")
	}
}

// TestLatestOutboundRefsMatchesWindowBoundsGivenInANonUTCZone covers the stamp
// lookup's own half of the same trap. The window bounds are derived from the
// arrival's timestamp, so they inherit whatever location that time carried.
func TestLatestOutboundRefsMatchesWindowBoundsGivenInANonUTCZone(t *testing.T) {
	repo, pool, ctx := newTestRepo(t)

	phone := "628123456789"
	// 2026-08-30T05:00Z sits INSIDE the 24h window below (which spans
	// 2026-08-30T01:00Z .. 2026-08-31T01:00Z), but only once the bounds are
	// compared as UTC. Un-normalized, notBefore renders "2026-08-30T08:00:00+07:00"
	// and "2026-08-30T05..." < "2026-08-30T08..." lexicographically, so the row
	// falls out of the window and the reply goes unstamped — the exact silent
	// mis-attribution this bound exists to prevent.
	outboundAt := time.Date(2026, 8, 30, 5, 0, 0, 0, time.UTC)
	seedOutbound(t, ctx, pool, nextID("wibout"), phone, "client-abc", "tenant-xyz", outboundAt)

	// The arrival, expressed in +07:00: 08:00 WIB == 01:00Z on the 31st.
	arrival := time.Date(2026, 8, 31, 8, 0, 0, 0, wib)
	refs, found, err := repo.LatestOutboundRefs(ctx, phone, arrival.Add(-24*time.Hour), arrival)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no stamp found for a window given in +07:00 — the bounds must be " +
			"normalized to UTC before they are compared as strings")
	}
	if refs.ClientRef != "client-abc" || refs.TenantRef != "tenant-xyz" {
		t.Fatalf("refs = %+v, want client-abc/tenant-xyz", refs)
	}
}
