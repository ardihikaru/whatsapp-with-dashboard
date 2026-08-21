package message

import (
	"context"
	"fmt"
	"strings"
)

// The read side of the outbound audit log — the queries the /logs page and the
// dashboard render. All SQL here is deliberately portable between the production
// MySQL (laundry_wa) pool and the SQLite pool the unit tests use: day bounds go
// through SUBSTR(created_at,1,10) (created_at is a fixed-width RFC3339 UTC string,
// so a 10-char prefix is the calendar day and sorts correctly), and status splits
// use COUNT(CASE WHEN ...) which returns a plain integer on both engines.

// Filter narrows an analytics query. Empty string fields are ignored; the day
// bounds are inclusive calendar days ("2006-01-02").
type Filter struct {
	Status, SessionID, APIClientID string
	ClientRef, TenantRef, Category string
	FromDay, ToDay                 string
}

// where builds the shared WHERE clause and its args. The table is always aliased
// `m`, so callers that JOIN (List) and callers that do not (Totals/Breakdown/Series)
// share one builder.
func (f Filter) where() (string, []any) {
	var c []string
	var a []any
	eq := func(col, v string) {
		if v != "" {
			c = append(c, col+" = ?")
			a = append(a, v)
		}
	}
	eq("m.status", f.Status)
	eq("m.session_id", f.SessionID)
	eq("m.api_client_id", f.APIClientID)
	eq("m.client_ref", f.ClientRef)
	eq("m.tenant_ref", f.TenantRef)
	eq("m.category", f.Category)
	if f.FromDay != "" {
		c = append(c, "SUBSTR(m.created_at,1,10) >= ?")
		a = append(a, f.FromDay)
	}
	if f.ToDay != "" {
		c = append(c, "SUBSTR(m.created_at,1,10) <= ?")
		a = append(a, f.ToDay)
	}
	if len(c) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(c, " AND "), a
}

// Totals is the general summary: the grand total plus the per-status split.
type Totals struct{ Total, Queued, Sent, Failed int }

// Totals counts outbound messages by status under the filter.
func (r *SQLRepository) Totals(ctx context.Context, f Filter) (Totals, error) {
	where, args := f.where()
	rows, err := r.pool.QueryContext(ctx,
		`SELECT m.status, COUNT(*) FROM wa_messages m`+where+` GROUP BY m.status`, args...)
	if err != nil {
		return Totals{}, err
	}
	defer rows.Close()
	var t Totals
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return Totals{}, err
		}
		t.Total += n
		switch status {
		case StatusQueued:
			t.Queued = n
		case StatusSent:
			t.Sent = n
		case StatusFailed:
			t.Failed = n
		}
	}
	return t, rows.Err()
}

// BreakdownRow is one group of a Breakdown: the raw column value (Key, "" means
// un-attributed) and its status split.
type BreakdownRow struct {
	Key                 string
	Total, Sent, Failed int
}

// breakdownCols is the closed set of columns a Breakdown may group by. The map is
// the ONLY thing that reaches the query as an identifier — an unknown dimension is
// rejected, never interpolated, so the group-by can never carry injected SQL.
var breakdownCols = map[string]string{
	"client_ref":    "m.client_ref",
	"tenant_ref":    "m.tenant_ref",
	"api_client_id": "m.api_client_id",
	"session_id":    "m.session_id",
	"status":        "m.status",
	"category":      "m.category",
}

// Breakdown groups outbound volume by one attribution dimension, busiest first.
func (r *SQLRepository) Breakdown(ctx context.Context, f Filter, dimension string, limit int) ([]BreakdownRow, error) {
	col, ok := breakdownCols[dimension]
	if !ok {
		return nil, fmt.Errorf("message: unknown breakdown dimension %q", dimension)
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	where, args := f.where()
	q := `SELECT COALESCE(` + col + `,''), COUNT(*),
	        COUNT(CASE WHEN m.status='sent' THEN 1 END),
	        COUNT(CASE WHEN m.status='failed' THEN 1 END)
	      FROM wa_messages m` + where + `
	      GROUP BY COALESCE(` + col + `,'')
	      ORDER BY COUNT(*) DESC, 1 ASC LIMIT ?`
	args = append(args, limit)
	rows, err := r.pool.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BreakdownRow
	for rows.Next() {
		var b BreakdownRow
		if err := rows.Scan(&b.Key, &b.Total, &b.Sent, &b.Failed); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
