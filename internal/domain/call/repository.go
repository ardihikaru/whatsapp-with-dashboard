package call

import (
	"context"
	"database/sql"
	"time"
)

// Repository is the persistence seam for placed calls: Insert from the
// handler that places one, the three Mark* transitions from wherever waxum's
// result (or the terminate call) lands, and Recent for the calls log page.
type Repository interface {
	Insert(ctx context.Context, c *Call) error // status=placing
	MarkPlaced(ctx context.Context, id, callID, recordingURL string) error
	MarkFailed(ctx context.Context, id, errMsg string) error
	MarkTerminated(ctx context.Context, id string) error
	// MarkTerminatedByCallID flips a row to terminated keyed by WAXUM'S
	// call_id, not our own row id — see this method's call site
	// (api_calls.go's PostAPICallTerminate) for why: that handler is only
	// ever given waxum's call_id, never our row id, so MarkTerminated
	// alone left every terminated call showing "placed" forever. Scoped by
	// sessionID so one session can never terminate another session's row
	// by guessing/reusing a call_id.
	MarkTerminatedByCallID(ctx context.Context, sessionID, callID string) error
	Recent(ctx context.Context, sessionID string, limit int) ([]Call, error)
}

// SQLRepository implements Repository on the shared SQLite/MySQL pool.
type SQLRepository struct{ pool *sql.DB }

func NewRepository(pool *sql.DB) *SQLRepository { return &SQLRepository{pool: pool} }

const timeLayout = time.RFC3339

// nullIfEmpty stores "" as SQL NULL, matching internal/domain/message's
// helper of the same name and for the same reason: an absent value should be
// genuinely absent, not an empty string masquerading as a real one.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (r *SQLRepository) Insert(ctx context.Context, c *Call) error {
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if c.Status == "" {
		c.Status = StatusPlacing
	}
	_, err := r.pool.ExecContext(ctx,
		`INSERT INTO wa_calls
		   (id, session_id, api_client_id, to_phone, kind, text, voice, audio_url,
		    call_id, recording_url, status, error, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.SessionID, nullIfEmpty(c.APIClientID), c.ToPhone, c.Kind,
		nullIfEmpty(c.Text), nullIfEmpty(c.Voice), nullIfEmpty(c.AudioURL),
		c.CallID, c.RecordingURL, c.Status, c.Error, c.CreatedAt.Format(timeLayout))
	return err
}

func (r *SQLRepository) MarkPlaced(ctx context.Context, id, callID, recordingURL string) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_calls SET status = ?, call_id = ?, recording_url = ? WHERE id = ?`,
		StatusPlaced, callID, recordingURL, id)
	return err
}

func (r *SQLRepository) MarkFailed(ctx context.Context, id, errMsg string) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_calls SET status = ?, error = ? WHERE id = ?`,
		StatusFailed, errMsg, id)
	return err
}

func (r *SQLRepository) MarkTerminated(ctx context.Context, id string) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_calls SET status = ?, ended_at = ? WHERE id = ?`,
		StatusTerminated, time.Now().UTC().Format(timeLayout), id)
	return err
}

func (r *SQLRepository) MarkTerminatedByCallID(ctx context.Context, sessionID, callID string) error {
	_, err := r.pool.ExecContext(ctx,
		`UPDATE wa_calls SET status = ?, ended_at = ? WHERE session_id = ? AND call_id = ?`,
		StatusTerminated, time.Now().UTC().Format(timeLayout), sessionID, callID)
	return err
}

func (r *SQLRepository) Recent(ctx context.Context, sessionID string, limit int) ([]Call, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := r.pool.QueryContext(ctx,
		`SELECT id, session_id, api_client_id, to_phone, kind, text, voice, audio_url,
		        call_id, recording_url, status, error, created_at, ended_at
		 FROM wa_calls WHERE session_id = ?
		 ORDER BY created_at DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Call
	for rows.Next() {
		var c Call
		var apiClientID, text, voice, audioURL sql.NullString
		var created string
		var ended sql.NullString
		if err := rows.Scan(&c.ID, &c.SessionID, &apiClientID, &c.ToPhone, &c.Kind,
			&text, &voice, &audioURL, &c.CallID, &c.RecordingURL, &c.Status, &c.Error,
			&created, &ended); err != nil {
			return nil, err
		}
		c.APIClientID = apiClientID.String
		c.Text = text.String
		c.Voice = voice.String
		c.AudioURL = audioURL.String
		c.CreatedAt, _ = time.Parse(timeLayout, created)
		if ended.Valid {
			if t, err := time.Parse(timeLayout, ended.String); err == nil {
				c.EndedAt = &t
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
