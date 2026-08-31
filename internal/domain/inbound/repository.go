package inbound

import (
	"context"
	"database/sql"
	"time"
)

// Repository is the persistence seam for customer replies: Insert from the
// event-consuming path (Stage 2), Since for the GET /api/v1/inbound handler
// (Stage 3), and LatestOutboundRefs for the client_ref/tenant_ref stamp.
type Repository interface {
	Insert(ctx context.Context, m *Message) error
	Since(ctx context.Context, t time.Time, limit int) ([]Message, error)
	LatestOutboundRefs(ctx context.Context, phone string, notBefore, notAfter time.Time) (Refs, bool, error)
}

// SQLRepository implements Repository on the shared SQLite/MySQL pool.
type SQLRepository struct{ pool *sql.DB }

func NewRepository(pool *sql.DB) *SQLRepository { return &SQLRepository{pool: pool} }

const timeLayout = time.RFC3339

// nullIfEmpty stores "" as SQL NULL, matching internal/domain/call and
// internal/domain/message's helper of the same name and for the same
// reason: an absent value should be genuinely absent, not an empty string
// masquerading as a real one.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Insert stores an arrival, and must be portable across MySQL (production)
// and SQLite (tests, e2e) — no repository under internal/domain/ carries
// dialect-specific SQL today, and the reference implementation's
// INSERT IGNORE is MySQL-only. Instead: try the plain INSERT; if it fails,
// check whether a row with this id already exists and, if so, treat the
// failure as a harmless redelivery (the event stream is lossy by design —
// see CLAUDE.md's SSE-event hazards) rather than an error. This costs one
// round trip on the happy path (the common case: a new id) and two only on
// the redelivery case.
func (r *SQLRepository) Insert(ctx context.Context, m *Message) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := r.pool.ExecContext(ctx,
		`INSERT INTO wa_inbound_messages
		   (id, session_id, from_phone, from_jid, from_lid, body, received_at,
		    client_ref, tenant_ref, replies_to_message_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.SessionID, m.FromPhone, m.FromJID, nullIfEmpty(m.FromLID),
		nullIfEmpty(m.Body), m.ReceivedAt.UTC().Format(timeLayout),
		nullIfEmpty(m.ClientRef), nullIfEmpty(m.TenantRef),
		nullIfEmpty(m.RepliesToMessageID), m.CreatedAt.UTC().Format(timeLayout))
	if err == nil {
		return nil
	}
	var exists int
	checkErr := r.pool.QueryRowContext(ctx,
		`SELECT 1 FROM wa_inbound_messages WHERE id = ?`, m.ID).Scan(&exists)
	if checkErr == nil {
		// Row already there — a redelivery of the same WhatsApp message id,
		// not a real failure.
		return nil
	}
	return err
}

// Since returns arrivals at or after t, OLDEST FIRST. This ordering is
// behavioural, not cosmetic: the consumer resolves a reply against the open
// question it answers, and replaying newest-first (or unordered) would let a
// later message close a question an earlier one already answered.
//
// limit is clamped the same way internal/domain/message and
// internal/domain/call clamp theirs, but to this endpoint's own numbers per
// the brief: <=0 or >500 becomes 200.
func (r *SQLRepository) Since(ctx context.Context, t time.Time, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT id, session_id, from_phone, from_jid,
		        COALESCE(from_lid, ''), COALESCE(body, ''), received_at,
		        COALESCE(client_ref, ''), COALESCE(tenant_ref, ''),
		        COALESCE(replies_to_message_id, ''), created_at
		 FROM wa_inbound_messages
		 WHERE received_at >= ?
		 ORDER BY received_at ASC, id ASC
		 LIMIT ?`, t.UTC().Format(timeLayout), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var received, created string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.FromPhone, &m.FromJID,
			&m.FromLID, &m.Body, &received,
			&m.ClientRef, &m.TenantRef, &m.RepliesToMessageID, &created); err != nil {
			return nil, err
		}
		m.ReceivedAt, _ = time.Parse(timeLayout, received)
		m.CreatedAt, _ = time.Parse(timeLayout, created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// LatestOutboundRefs reads wa_messages (NOT wa_inbound_messages) for the
// most recent outbound send to phone, and returns its client_ref/tenant_ref
// as the stamp for an arrival believed to be that send's reply.
//
// Both notBefore AND notAfter bound the window. notAfter is the deliberate
// departure from the reference implementation, which anchors its 24h window
// on time.Now() — safe only for a live event handler that stamps an arrival
// the instant it happens. This gateway instead POLLS a backlog (waxum's SSE
// tail can be consumed after a gap — see CLAUDE.md's SSE-event hazards), so
// without notAfter a three-day-old arrival being processed just now would
// match an outbound sent two hours ago and get stamped with the WRONG
// order's refs. notAfter must be the arrival's own ReceivedAt, not
// time.Now(), so the window is always anchored to when the customer
// actually replied.
//
// An empty phone returns (Refs{}, false, nil) WITHOUT querying — a LID that
// failed to resolve to a phone number must never accidentally match a
// wa_messages row whose to_phone happens to be the empty string (e.g. a row inserted
// before to_phone was populated, or a data bug). This is the security
// property under test: an unresolved sender must never be attributed to
// anyone else's order.
func (r *SQLRepository) LatestOutboundRefs(ctx context.Context, phone string, notBefore, notAfter time.Time) (Refs, bool, error) {
	if phone == "" {
		return Refs{}, false, nil
	}
	var refs Refs
	err := r.pool.QueryRowContext(ctx,
		`SELECT COALESCE(client_ref, ''), COALESCE(tenant_ref, ''), id
		 FROM wa_messages
		 WHERE to_phone = ? AND created_at >= ? AND created_at <= ?
		 ORDER BY created_at DESC LIMIT 1`,
		phone, notBefore.UTC().Format(timeLayout), notAfter.UTC().Format(timeLayout),
	).Scan(&refs.ClientRef, &refs.TenantRef, &refs.MessageID)
	if err == sql.ErrNoRows {
		return Refs{}, false, nil
	}
	if err != nil {
		return Refs{}, false, err
	}
	return refs, true, nil
}
