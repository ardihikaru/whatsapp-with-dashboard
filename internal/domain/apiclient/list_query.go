package apiclient

import (
	"context"
	"strings"
)

// The paged read side of /admin/api-clients. List() (repository.go) still exists
// for callers that genuinely need every client — the /logs page resolves consumer
// names from it — but the admin table must not grow unbounded with the table.

const listCols = `id, name, login_id, description, secret_hash, active,
	        token_ttl_seconds, no_expiry, created_at, updated_at`

// searchWhere builds the optional name/login_id/description match. An empty
// search returns no clause so the common case stays a plain scan.
func searchWhere(search string) (string, []any) {
	search = strings.TrimSpace(search)
	if search == "" {
		return "", nil
	}
	like := "%" + search + "%"
	return ` WHERE (name LIKE ? OR login_id LIKE ? OR description LIKE ?)`,
		[]any{like, like, like}
}

// CountList returns how many clients match the search.
func (r *Repository) CountList(ctx context.Context, search string) (int, error) {
	where, args := searchWhere(search)
	var n int
	err := r.pool.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_clients`+where, args...).Scan(&n)
	return n, err
}

// ListPage returns one newest-first page of clients matching the search.
func (r *Repository) ListPage(ctx context.Context, search string, limit, offset int) ([]Client, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where, args := searchWhere(search)
	args = append(args, limit, offset)
	rows, err := r.pool.QueryContext(ctx,
		`SELECT `+listCols+` FROM api_clients`+where+
			` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Client, 0, limit)
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
