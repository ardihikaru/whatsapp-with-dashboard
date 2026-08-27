package waxum

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// eventScannerInitialBufferSize/eventScannerMaxBufferSize replace
// bufio.Scanner's default 64 KiB token cap, which would otherwise silently
// TRUNCATE a large "data:" line and report the problem only via Scanner.Err
// after the fact — TestTailEventsHandlesADataLineLargerThanBufioDefaultTokenSize
// is the regression test.
const (
	eventScannerInitialBufferSize = 64 << 10
	eventScannerMaxBufferSize     = 1 << 20
)

// Event is one parsed Server-Sent Event off GET /api/v1/events/tail. Type is
// the SSE "event:" field's value, ID is the SSE "id:" field (waxum sends
// at_epoch_ms as a string there), and Data is the raw "data:" payload —
// callers decode it into whatever shape the event Type implies.
type Event struct {
	Type string
	ID   string
	Data json.RawMessage
}

// TailOptions filters TailEvents' stream. Both are optional; an empty value
// omits the corresponding query parameter, so the stream is unfiltered.
type TailOptions struct {
	SessionID string
	EventName string
}

// TailEvents opens waxum's fleet-wide SSE event stream and calls fn once per
// event, blocking until ctx is cancelled, fn returns an error, or the
// connection drops. Callers reconnect with their own backoff — this method
// makes exactly one connection attempt.
//
// Four things here are load-bearing and each has its own test:
//
//  1. This does NOT use c.http. New builds that client with Timeout set to
//     WSM_WAXUM_TIMEOUT (30s by default, client.go's New), and
//     http.Client.Timeout bounds the ENTIRE request including the body
//     read — so a long-lived stream on c.http would be killed every 30s.
//     TailEvents builds its own *http.Client with Timeout: 0, bounded only
//     by ctx.
//  2. This does NOT go through do()/send(). Both io.ReadAll a
//     LimitReader — correct for a bounded JSON response, wrong for a body
//     that is deliberately infinite until the caller stops reading.
//  3. bufio.Scanner's default 64 KiB token cap is raised (see the consts
//     above) — a real event that exceeds it would otherwise be silently
//     truncated and only reported via Scanner.Err after the fact.
//  4. ":"-prefixed comment lines, including waxum's literal "keep-alive"
//     frames, are skipped rather than treated as event data.
//
// TailEvents also does NOT go through c.limiter — that bucket exists to cap
// a BURST of short-lived requests (ratelimit.go's doc comment); a single
// long-lived connection is not the traffic it was sized for.
func (c *Client) TailEvents(ctx context.Context, opts TailOptions, fn func(Event) error) error {
	q := url.Values{}
	if opts.SessionID != "" {
		q.Set("session", opts.SessionID)
	}
	if opts.EventName != "" {
		q.Set("event", opts.EventName)
	}
	path := "/api/v1/events/tail"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("waxum: building events request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.token)

	// A dedicated client with NO timeout — see point 1 above. Built fresh
	// per call rather than stored on Client so nothing else in this
	// package is tempted to reuse it for an ordinary bounded request.
	streamClient := &http.Client{Timeout: 0}
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("waxum: events tail: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errorFromStatus(resp.StatusCode, nil)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, eventScannerInitialBufferSize), eventScannerMaxBufferSize)

	var cur Event
	var haveData bool
	dispatch := func() error {
		if !haveData && cur.Type == "" && cur.ID == "" {
			return nil // a blank line with nothing accumulated — nothing to dispatch
		}
		defer func() { cur = Event{}; haveData = false }()
		if cur.Type == "keep-alive" {
			return nil
		}
		return fn(cur)
	}

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// SSE comment line — waxum's keep-alive frames are sent this
			// way too in some builds; either form is skipped.
			continue
		case strings.HasPrefix(line, "event:"):
			cur.Type = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "id:"):
			cur.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ")
			if haveData {
				cur.Data = append(append(cur.Data, '\n'), []byte(data)...)
			} else {
				cur.Data = json.RawMessage(data)
				haveData = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("waxum: reading events stream: %w", err)
	}
	// A final dispatch in case the stream ended right after a data line
	// with no trailing blank line.
	return dispatch()
}
