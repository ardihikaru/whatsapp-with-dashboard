package waxum

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// errStopTail is a sentinel TailEvents' fn callback returns to end the
// stream deliberately once a test has seen what it needs — TailEvents
// propagates fn's error verbatim, so this doubles as proof that it does.
var errStopTail = errors.New("stop tail: test is done")

func TestTailEventsParsesEventIDAndDataAcrossBlankLineDelimitedFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: session.connected\nid: 1700000000000\ndata: {\"session_id\":\"s1\"}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: session.disconnected\nid: 1700000000123\ndata: {\"session_id\":\"s1\",\"reason\":\"logout\"}\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, testToken, 5*time.Second)

	var got []Event
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.TailEvents(ctx, TailOptions{}, func(e Event) error {
		got = append(got, e)
		if len(got) == 2 {
			return errStopTail
		}
		return nil
	})
	if !errors.Is(err, errStopTail) {
		t.Fatalf("TailEvents returned %v, want the sentinel that stops the loop", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Type != "session.connected" || got[0].ID != "1700000000000" {
		t.Errorf("got[0] = %+v, want Type=session.connected ID=1700000000000", got[0])
	}
	if string(got[0].Data) != `{"session_id":"s1"}` {
		t.Errorf("got[0].Data = %s, want the raw data line unchanged", got[0].Data)
	}
	if got[1].Type != "session.disconnected" {
		t.Errorf("got[1].Type = %q, want %q", got[1].Type, "session.disconnected")
	}
}

func TestTailEventsIgnoresKeepAliveFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, ": this is a comment line, not an event\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: keep-alive\ndata: {}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: session.connected\nid: 1\ndata: {\"session_id\":\"s1\"}\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, testToken, 5*time.Second)

	var got []Event
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.TailEvents(ctx, TailOptions{}, func(e Event) error {
		got = append(got, e)
		return errStopTail
	})
	if !errors.Is(err, errStopTail) {
		t.Fatalf("TailEvents returned %v, want the sentinel", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d dispatched events, want 1 — comment lines and keep-alive frames must not be dispatched", len(got))
	}
	if got[0].Type != "session.connected" {
		t.Errorf("got[0].Type = %q, want %q", got[0].Type, "session.connected")
	}
}

func TestTailEventsHandlesADataLineLargerThanBufioDefaultTokenSize(t *testing.T) {
	big := strings.Repeat("x", 100<<10) // 100 KiB > bufio.Scanner's 64 KiB default token cap
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "event: big\nid: 1\ndata: %s\n\n", big)
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	client := New(srv.URL, testToken, 5*time.Second)

	var got Event
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.TailEvents(ctx, TailOptions{}, func(e Event) error {
		got = e
		return errStopTail
	})
	if !errors.Is(err, errStopTail) {
		t.Fatalf("TailEvents returned %v, want the sentinel", err)
	}
	if len(got.Data) != len(big) {
		t.Fatalf("event data length = %d, want %d — a large data line must not be truncated by "+
			"bufio.Scanner's default 64 KiB token cap", len(got.Data), len(big))
	}
}

// TestTailEventsDoesNotUseThePerRequestTimeoutClient proves TailEvents does
// not read the SSE body through c.http: New's http.Client.Timeout bounds the
// ENTIRE request including the body read, so a stream on that client would
// die at 50ms — well before this fake server's event arrives at ~200ms.
func TestTailEventsDoesNotUseThePerRequestTimeoutClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		flusher.Flush()
		time.Sleep(200 * time.Millisecond)
		fmt.Fprint(w, "event: session.connected\nid: 1\ndata: {\"session_id\":\"s1\"}\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, testToken, 50*time.Millisecond)

	var got Event
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := client.TailEvents(ctx, TailOptions{}, func(e Event) error {
		got = e
		return errStopTail
	})
	if !errors.Is(err, errStopTail) {
		t.Fatalf("TailEvents returned %v, want the sentinel — it must survive past the client's 50ms "+
			"per-request timeout to see an event that arrives at ~200ms", err)
	}
	if got.Type != "session.connected" {
		t.Errorf("got.Type = %q, want %q", got.Type, "session.connected")
	}
}
