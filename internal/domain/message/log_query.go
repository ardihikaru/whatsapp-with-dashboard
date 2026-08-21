package message

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// LogRow is one row of the /logs table: the message plus the session's display
// name and owner type, resolved by a LEFT JOIN so a send whose session was later
// deleted still lists (with an empty session name) rather than vanishing.
type LogRow struct {
	Message
	SessionName string
	OwnerType   string
	rawCreated  string // on-the-wire created_at, kept byte-exact for the cursor
}

const logCols = `m.id, m.session_id, m.api_client_id, m.client_ref, m.tenant_ref,
	m.category, m.to_phone, m.body, m.status, m.wa_message_id, m.error, m.attempts,
	m.created_at, m.sent_at, COALESCE(s.name,''), COALESCE(s.owner_type,'')`

// cursorSep joins created_at and id into one opaque keyset cursor. \x1f (unit
// separator) can never appear in an RFC3339 timestamp or a wa.NewID, so a split is
// unambiguous.
const cursorSep = "\x1f"

// List returns outbound messages newest-first under the filter, keyset-paginated.
// It fetches limit+1 rows: if the extra row exists there is another page, and the
// returned cursor is the token to pass back as `cursor` for it ("" = last page).
func (r *SQLRepository) List(ctx context.Context, f Filter, cursor string, limit int) ([]LogRow, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where, args := f.where()
	if cursor != "" {
		created, id, _ := strings.Cut(cursor, cursorSep)
		clause := "(m.created_at < ? OR (m.created_at = ? AND m.id < ?))"
		if where == "" {
			where = " WHERE " + clause
		} else {
			where += " AND " + clause
		}
		args = append(args, created, created, id)
	}
	q := `SELECT ` + logCols + `
	      FROM wa_messages m LEFT JOIN wa_sessions s ON s.id = m.session_id` +
		where + ` ORDER BY m.created_at DESC, m.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.pool.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := make([]LogRow, 0, limit+1)
	for rows.Next() {
		lr, created, err := scanLogRow(rows)
		if err != nil {
			return nil, "", err
		}
		lr.rawCreated = created
		out = append(out, lr)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		last := out[limit-1]
		next = last.rawCreated + cursorSep + last.ID
		out = out[:limit]
	}
	return out, next, nil
}

func scanLogRow(rows *sql.Rows) (LogRow, string, error) {
	var lr LogRow
	var apiClient, clientRef, tenantRef, category, created, sent sql.NullString
	if err := rows.Scan(&lr.ID, &lr.SessionID, &apiClient, &clientRef, &tenantRef,
		&category, &lr.ToPhone, &lr.Body, &lr.Status, &lr.WAMessageID, &lr.Error,
		&lr.Attempts, &created, &sent, &lr.SessionName, &lr.OwnerType); err != nil {
		return LogRow{}, "", err
	}
	lr.APIClientID = apiClient.String
	lr.ClientRef = clientRef.String
	lr.TenantRef = tenantRef.String
	lr.Category = category.String
	lr.CreatedAt, _ = time.Parse(timeLayout, created.String)
	if sent.Valid {
		if t, err := time.Parse(timeLayout, sent.String); err == nil {
			lr.SentAt = &t
		}
	}
	return lr, created.String, nil
}
