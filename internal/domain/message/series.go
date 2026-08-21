package message

import (
	"context"
	"sort"
	"time"
)

// DailyPoint is one calendar day's outbound volume split.
type DailyPoint struct {
	Day                 string // "2006-01-02"
	Total, Sent, Failed int
}

// Series returns the last `days` calendar days ending today (UTC), gap-filled so
// the chart axis is continuous even on days with no traffic.
func (r *SQLRepository) Series(ctx context.Context, f Filter, days int) ([]DailyPoint, error) {
	if days <= 0 || days > 366 {
		days = 30
	}
	today := time.Now().UTC()
	start := today.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	f.FromDay = maxDay(f.FromDay, start)

	where, args := f.where()
	rows, err := r.pool.QueryContext(ctx,
		`SELECT SUBSTR(m.created_at,1,10) d, COUNT(*),
		        COUNT(CASE WHEN m.status='sent' THEN 1 END),
		        COUNT(CASE WHEN m.status='failed' THEN 1 END)
		 FROM wa_messages m`+where+` GROUP BY d`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[string]DailyPoint{}
	for rows.Next() {
		var p DailyPoint
		if err := rows.Scan(&p.Day, &p.Total, &p.Sent, &p.Failed); err != nil {
			return nil, err
		}
		byDay[p.Day] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DailyPoint, days)
	for i := 0; i < days; i++ {
		day := today.AddDate(0, 0, -(days - 1 - i)).Format("2006-01-02")
		if p, ok := byDay[day]; ok {
			out[i] = p
		} else {
			out[i] = DailyPoint{Day: day}
		}
	}
	return out, nil
}

// LatencyPoint is one hour-of-day bucket's send latency percentiles (ms).
type LatencyPoint struct {
	Hour     string // "15:00"
	P50, P95 int
}

// Latency reads sent messages from the last 24h and buckets their send latency
// (sent_at − created_at) by hour-of-day, computing p50/p95 per bucket in Go —
// percentiles are awkward and non-portable in SQL, and the row count is bounded.
func (r *SQLRepository) Latency(ctx context.Context, f Filter) ([]LatencyPoint, error) {
	f.Status = StatusSent
	f.FromDay = maxDay(f.FromDay, time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"))
	where, args := f.where()
	rows, err := r.pool.QueryContext(ctx,
		`SELECT m.created_at, m.sent_at FROM wa_messages m`+where+
			` AND m.sent_at IS NOT NULL LIMIT 20000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buckets := make([][]int, 24)
	for rows.Next() {
		var created, sent string
		if err := rows.Scan(&created, &sent); err != nil {
			return nil, err
		}
		c, err1 := time.Parse(timeLayout, created)
		s, err2 := time.Parse(timeLayout, sent)
		if err1 != nil || err2 != nil || !s.After(c) {
			continue
		}
		buckets[c.Hour()] = append(buckets[c.Hour()], int(s.Sub(c).Milliseconds()))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]LatencyPoint, 24)
	for h := 0; h < 24; h++ {
		out[h] = LatencyPoint{
			Hour: time.Date(0, 1, 1, h, 0, 0, 0, time.UTC).Format("15:00"),
			P50:  percentile(buckets[h], 0.50),
			P95:  percentile(buckets[h], 0.95),
		}
	}
	return out, nil
}

// percentile returns the p-quantile (0..1) of xs by nearest-rank; 0 for empty.
func percentile(xs []int, p float64) int {
	if len(xs) == 0 {
		return 0
	}
	sort.Ints(xs)
	rank := int(p * float64(len(xs)-1))
	return xs[rank]
}

// maxDay returns the later of two "2006-01-02" day strings (lexicographic ==
// chronological for that fixed format); "" means unbounded, so any real day wins.
func maxDay(a, b string) string {
	if a == "" || b > a {
		return b
	}
	return a
}
