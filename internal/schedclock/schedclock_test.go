package schedclock

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestParseClockAcceptsOnlyStrictHHMM(t *testing.T) {
	for _, ok := range []string{"00:00", "07:00", "10:27", "23:59"} {
		if _, err := ParseClock(ok); err != nil {
			t.Errorf("ParseClock(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "7:00", "24:00", "10:60", "10-27", "ab:cd", "10:27:00"} {
		if _, err := ParseClock(bad); err == nil {
			t.Errorf("ParseClock(%q) accepted, want error", bad)
		}
	}
}

func TestValidateWindowRequiresEndStrictlyAfterStart(t *testing.T) {
	if err := ValidateWindow("07:00", "10:27"); err != nil {
		t.Fatalf("07:00-10:27 rejected: %v", err)
	}
	for _, c := range [][2]string{{"10:27", "07:00"}, {"08:00", "08:00"}, {"xx", "09:00"}, {"08:00", "99:00"}} {
		if err := ValidateWindow(c[0], c[1]); err == nil {
			t.Errorf("ValidateWindow(%q,%q) accepted, want error", c[0], c[1])
		}
	}
}

func TestFiresOnMatchesTheISOWeekdayOfTheLocalDay(t *testing.T) {
	// 2026-11-02 is a Monday, 2026-11-01 a Sunday (stored as 7).
	mon := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	sun := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if !FiresOn("1,3", mon) || FiresOn("3,5", mon) {
		t.Error("Monday must match 1 and not 3,5")
	}
	if !FiresOn("7", sun) || FiresOn("1,2,3,4,5,6", sun) {
		t.Error("Sunday must match 7 and nothing else")
	}
	if FiresOn("", mon) || FiresOn("x, ,", mon) {
		t.Error("blank/garbage csv must match nothing")
	}
}

func TestDrawNeverLeavesTheWindowAndCoversBothHalves(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, loc)
	lo, hi := Bounds(day, loc, Clock{7, 0}, Clock{10, 27})
	rng := rand.New(rand.NewPCG(1, 2))
	mid := lo.Add(hi.Sub(lo) / 2)
	early, late := 0, 0
	for i := 0; i < 5000; i++ {
		at := Draw(rng, lo, hi)
		if at.Before(lo) || at.After(hi) {
			t.Fatalf("draw %v outside [%v, %v]", at, lo, hi)
		}
		if at.Before(mid) {
			early++
		} else {
			late++
		}
	}
	if early < 2000 || late < 2000 {
		t.Errorf("draw is not roughly uniform: early=%d late=%d", early, late)
	}
}

func TestDrawOfADegenerateWindowReturnsItsStart(t *testing.T) {
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	if got := Draw(rand.New(rand.NewPCG(1, 2)), at, at.Add(-time.Hour)); !got.Equal(at) {
		t.Errorf("Draw with hi<lo = %v, want %v", got, at)
	}
}

func TestBoundsFollowTheLocalCalendarDayNotUTC(t *testing.T) {
	// 2026-11-02 20:00Z is already 03:00 on Nov 3 in Jakarta (UTC+7).
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 11, 2, 20, 0, 0, 0, time.UTC)
	if got := LocalDate(now, loc); got != "2026-11-03" {
		t.Fatalf("LocalDate = %s, want 2026-11-03", got)
	}
	lo, _ := Bounds(now, loc, Clock{7, 0}, Clock{8, 0})
	if want := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC); !lo.Equal(want) {
		t.Errorf("window start = %v, want %v (07:00 WIB on Nov 3)", lo.UTC(), want)
	}
}

func TestBoundsSurviveASpringForwardGap(t *testing.T) {
	// America/New_York 2026-03-08: 02:00 -> 03:00, so 02:30 does not exist.
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	day := time.Date(2026, 3, 8, 12, 0, 0, 0, loc)
	lo, hi := Bounds(day, loc, Clock{1, 30}, Clock{3, 30})
	if !hi.After(lo) {
		t.Fatalf("window collapsed: %v .. %v", lo, hi)
	}
	rng := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 500; i++ {
		if at := Draw(rng, lo, hi); at.Before(lo) || at.After(hi) {
			t.Fatalf("draw %v escaped the DST-day window", at)
		}
	}
}
