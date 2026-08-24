package wa

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRingWraparound(t *testing.T) {
	r := NewRing(256)
	for i := 0; i < 300; i++ {
		r.Append(Event{At: time.Unix(int64(i), 0), Note: fmt.Sprintf("e%d", i)})
	}
	snap := r.Snapshot()
	if len(snap) != 256 {
		t.Fatalf("len = %d, want 256", len(snap))
	}
	if snap[0].Note != "e44" || snap[255].Note != "e299" {
		t.Fatalf("window = [%s … %s], want [e44 … e299]", snap[0].Note, snap[255].Note)
	}
	for i := 1; i < len(snap); i++ {
		if snap[i].At.Before(snap[i-1].At) {
			t.Fatalf("snapshot not chronological at %d", i)
		}
	}
}

// TestTheEventRingKeepsOnlyTheMostRecentEntries extends the coverage above
// (TestRingWraparound already pins Ring's own drop-oldest/fixed-capacity
// behaviour) to the app-wide diagnostic ring the system log page reads —
// Manager.RecentEvents, backed by manager.go's sysRing (same Ring type,
// sized sysRingSize instead of one session's ringSize). It reuses Ring
// unchanged; what's new here is RecentEvents' own contract: capped at
// sysRingSize AND returned NEWEST first (Ring.Snapshot itself stays
// oldest-first, unchanged — see TestRingWraparound).
func TestTheEventRingKeepsOnlyTheMostRecentEntries(t *testing.T) {
	m := newTestManager(newStubSessions(), newStubMsgs())
	total := sysRingSize + 50
	for i := 0; i < total; i++ {
		m.RecordEvent("info", fmt.Sprintf("e%d", i))
	}

	all := m.RecentEvents(0)
	if len(all) != sysRingSize {
		t.Fatalf("RecentEvents(0) len = %d, want %d (sysRingSize)", len(all), sysRingSize)
	}
	if want := fmt.Sprintf("e%d", total-1); all[0].Note != want {
		t.Fatalf("RecentEvents[0].Note = %q, want %q — RecentEvents must return the newest entry first", all[0].Note, want)
	}
	if want := fmt.Sprintf("e%d", total-sysRingSize); all[len(all)-1].Note != want {
		t.Fatalf("RecentEvents[last].Note = %q, want %q (the oldest entry still inside the window after the older ones dropped)", all[len(all)-1].Note, want)
	}

	limited := m.RecentEvents(3)
	if len(limited) != 3 {
		t.Fatalf("RecentEvents(3) len = %d, want 3", len(limited))
	}
	for i, want := range []string{
		fmt.Sprintf("e%d", total-1),
		fmt.Sprintf("e%d", total-2),
		fmt.Sprintf("e%d", total-3),
	} {
		if limited[i].Note != want {
			t.Fatalf("RecentEvents(3)[%d].Note = %q, want %q", i, limited[i].Note, want)
		}
	}
}

func TestRingConcurrent(t *testing.T) {
	r := NewRing(256)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				r.Append(Event{At: time.Now(), Note: fmt.Sprintf("g%d-%d", g, i)})
				if i%100 == 0 {
					_ = r.Snapshot()
				}
			}
		}(g)
	}
	wg.Wait()
	if got := len(r.Snapshot()); got != 256 {
		t.Fatalf("final snapshot len = %d, want 256", got)
	}
}
