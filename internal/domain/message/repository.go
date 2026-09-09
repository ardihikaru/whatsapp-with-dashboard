package message

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound is returned by GetByID when id names no row — the "genuinely
// unknown" signal internal/selfconvo's Recover reconciliation relies on to
// fall back to "interrupted" rather than guessing (see runner.go's Recover).
var ErrNotFound = errors.New("message: not found")

// Repository is the audit-log seam used by HTTP handlers (Insert, Recent) and
// the send worker (MarkSent, MarkFailed).
type Repository interface {
	Insert(ctx context.Context, m *Message) error // status=queued
	MarkSent(ctx context.Context, id, waMessageID string, attempts int) error
	MarkFailed(ctx context.Context, id, errMsg string, attempts int) error
	Recent(ctx context.Context, sessionID string, limit int) ([]Message, error)
}

// SQLRepository implements Repository on the shared SQLite pool.
type SQLRepository struct{ pool *sql.DB }

func NewRepository(pool *sql.DB) *SQLRepository { return &SQLRepository{pool: pool} }

const timeLayout = time.RFC3339

// nullIfEmpty stores "" as SQL NULL so the attribution columns stay genuinely
// absent (and index/GROUP BY treat un-attributed rows as one NULL bucket) rather
// than as an empty-string value that would masquerade as a real ref.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullIfZero is nullIfEmpty's numeric sibling: a plain text message has
// MediaSize==0, and that must persist as SQL NULL — not the number 0 — so
// media_size stays genuinely absent for every non-media row, the same reason
// nullIfEmpty exists for the string columns.
func nullIfZero(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func (r *SQLRepository) Insert(ctx context.Context, m *Message) error {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	if m.Status == "" {
		m.Status = StatusQueued
	}
	_, err := r.pool.ExecContext(ctx,
		`INSERT INTO wa_messages
		   (id, session_id, api_client_id, client_ref, tenant_ref, category,
		    to_phone, body, status, created_at,
		    media_kind, media_filename, media_mime, media_size, media_object_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.SessionID, nullIfEmpty(m.APIClientID), nullIfEmpty(m.ClientRef),
		nullIfEmpty(m.TenantRef), nullIfEmpty(m.Category),
		m.ToPhone, m.Body, m.Status, m.CreatedAt.Format(timeLayout),
		nullIfEmpty(m.MediaKind), nullIfEmpty(m.MediaFilename), nullIfEmpty(m.MediaMime),
		nullIfZero(m.MediaSize), nullIfEmpty(m.MediaObjectKey))
	return err
}

func (r *SQLRepository) MarkSent(ctx context.Context, id, waMessageID string, attempts int) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_messages SET status = ?, wa_message_id = ?, attempts = ?, sent_at = ?
		 WHERE id = ?`,
		StatusSent, waMessageID, attempts, time.Now().UTC().Format(timeLayout), id)
	return err
}

func (r *SQLRepository) MarkFailed(ctx context.Context, id, errMsg string, attempts int) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_messages SET status = ?, error = ?, attempts = ? WHERE id = ?`,
		StatusFailed, errMsg, attempts, id)
	return err
}

// GetByID reads back one row by its primary key — internal/selfconvo's
// Recover reconciliation (runner.go) is the only production caller: it
// looks up the wa_messages row a cut-off turn's ClaimTurn already minted an
// id for, to learn whether the send actually landed before this app's own
// process died. Returns ErrNotFound rather than sql.ErrNoRows, matching
// every other domain repository's by-id lookup (e.g.
// internal/domain/convo.Repository.GetRun).
func (r *SQLRepository) GetByID(ctx context.Context, id string) (Message, error) {
	row := r.pool.QueryRowContext(ctx,
		`SELECT id, session_id, to_phone, body, status, wa_message_id, error, attempts,
		        created_at, sent_at,
		        media_kind, media_filename, media_mime, media_size, media_object_key
		 FROM wa_messages WHERE id = ?`, id)

	var m Message
	var created string
	var sent sql.NullString
	var mediaKind, mediaFilename, mediaMime, mediaObjectKey sql.NullString
	var mediaSize sql.NullInt64
	err := row.Scan(&m.ID, &m.SessionID, &m.ToPhone, &m.Body, &m.Status,
		&m.WAMessageID, &m.Error, &m.Attempts, &created, &sent,
		&mediaKind, &mediaFilename, &mediaMime, &mediaSize, &mediaObjectKey)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrNotFound
	}
	if err != nil {
		return Message{}, err
	}
	m.CreatedAt, _ = time.Parse(timeLayout, created)
	if sent.Valid {
		if t, err := time.Parse(timeLayout, sent.String); err == nil {
			m.SentAt = &t
		}
	}
	m.MediaKind = mediaKind.String
	m.MediaFilename = mediaFilename.String
	m.MediaMime = mediaMime.String
	m.MediaSize = mediaSize.Int64
	m.MediaObjectKey = mediaObjectKey.String
	return m, nil
}

func (r *SQLRepository) Recent(ctx context.Context, sessionID string, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT id, session_id, to_phone, body, status, wa_message_id, error, attempts,
		        created_at, sent_at,
		        media_kind, media_filename, media_mime, media_size, media_object_key
		 FROM wa_messages WHERE session_id = ?
		 ORDER BY created_at DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var created string
		var sent sql.NullString
		var mediaKind, mediaFilename, mediaMime, mediaObjectKey sql.NullString
		var mediaSize sql.NullInt64
		if err := rows.Scan(&m.ID, &m.SessionID, &m.ToPhone, &m.Body, &m.Status,
			&m.WAMessageID, &m.Error, &m.Attempts, &created, &sent,
			&mediaKind, &mediaFilename, &mediaMime, &mediaSize, &mediaObjectKey); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(timeLayout, created)
		if sent.Valid {
			if t, err := time.Parse(timeLayout, sent.String); err == nil {
				m.SentAt = &t
			}
		}
		m.MediaKind = mediaKind.String
		m.MediaFilename = mediaFilename.String
		m.MediaMime = mediaMime.String
		m.MediaSize = mediaSize.Int64
		m.MediaObjectKey = mediaObjectKey.String
		out = append(out, m)
	}
	return out, rows.Err()
}
