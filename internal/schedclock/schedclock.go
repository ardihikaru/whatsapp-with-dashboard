// Package schedclock holds the small wall-clock helpers a "once per selected
// weekday, at a random time inside a window" schedule needs: parsing an
// "HH:MM" clock, matching an ISO weekday against a stored CSV, and drawing
// one uniform-random instant inside a window on a given local day.
//
// internal/selfconvo and internal/groupbroadcast each carry their own
// unexported copies of these (scheduleFiresOn, parseClock, drawUniform).
// This package exists so NEW code stops adding a third copy; the two older
// planners are deliberately not refactored onto it.
package schedclock

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// DateLayout is the stored/<input type="date"> day shape.
const DateLayout = "2006-01-02"

// Clock is a wall-clock time of day, minute resolution.
type Clock struct {
	Hour, Min int
}

// ParseClock parses a strict "HH:MM" (24h, two digits each).
func ParseClock(s string) (Clock, error) {
	if len(s) != 5 || s[2] != ':' {
		return Clock{}, fmt.Errorf("schedclock: %q is not HH:MM", s)
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return Clock{}, fmt.Errorf("schedclock: %q is not a valid HH:MM time", s)
	}
	return Clock{Hour: h, Min: m}, nil
}

// ValidateWindow requires both ends to parse and end to be strictly after
// start (a window never spans midnight).
func ValidateWindow(start, end string) error {
	s, err := ParseClock(start)
	if err != nil {
		return fmt.Errorf("window start must be a valid HH:MM time")
	}
	e, err := ParseClock(end)
	if err != nil {
		return fmt.Errorf("window end must be a valid HH:MM time")
	}
	if e.Hour*60+e.Min <= s.Hour*60+s.Min {
		return fmt.Errorf("window end must be after window start")
	}
	return nil
}

// ISOWeekday is Monday=1 .. Sunday=7 (time.Weekday has Sunday=0).
func ISOWeekday(t time.Time) int {
	if wd := int(t.Weekday()); wd != 0 {
		return wd
	}
	return 7
}

// FiresOn reports whether day (already in the schedule's own zone) falls on
// one of the ISO weekdays in csv ("1,3,5"). Blank or unparseable entries are
// skipped, so an empty csv matches nothing.
func FiresOn(csv string, day time.Time) bool {
	want := strconv.Itoa(ISOWeekday(day))
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) == want {
			return true
		}
	}
	return false
}

// Bounds returns the instants window start and end fall on, on day's local
// calendar date in loc. Built with time.Date so a DST gap/overlap on that
// date is resolved by the stdlib, never by adding hours to midnight.
func Bounds(day time.Time, loc *time.Location, start, end Clock) (time.Time, time.Time) {
	y, m, d := day.In(loc).Date()
	return time.Date(y, m, d, start.Hour, start.Min, 0, 0, loc),
		time.Date(y, m, d, end.Hour, end.Min, 0, 0, loc)
}

// Draw returns one uniformly random whole-second instant in [lo, hi]
// (inclusive). hi before lo is clamped to lo.
func Draw(rng *rand.Rand, lo, hi time.Time) time.Time {
	span := int64(hi.Sub(lo) / time.Second)
	if span <= 0 {
		return lo
	}
	return lo.Add(time.Duration(rng.Int64N(span+1)) * time.Second)
}

// LocalDate formats t's calendar date in loc.
func LocalDate(t time.Time, loc *time.Location) string {
	return t.In(loc).Format(DateLayout)
}
