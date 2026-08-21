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
