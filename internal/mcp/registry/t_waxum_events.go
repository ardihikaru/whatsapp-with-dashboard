package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/mcp/upstream"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

func decodeBase64(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("could not base64-decode: %w", err)
	}
	return b, nil
}

const (
	defaultTailEvents = 20
	maxTailEvents     = 200
	defaultTailSecs   = 10
	maxTailSecs       = 60
)

// waxumEventsTailTool wraps waxum.Client.TailEvents, which has no plain
// return value (it blocks, calling fn per SSE event) and so cannot go
// through the generic reflect-based CallWaxum — it is bounded by
// max_events/seconds so a tool call can never block indefinitely.
func waxumEventsTailTool() Tool {
	return Tool{
		Name: "waxum_events_tail", WaxumMethod: "TailEvents", ReadOnly: true,
		Desc: "Tail waxum's fleet-wide SSE event stream for a bounded window, collecting up to max_events events or until seconds elapses, whichever comes first. Use to watch connection/pairing events live rather than polling status repeatedly.",
		Params: []Param{
			{Name: "session_id_filter", Type: "string", Desc: "Only events for this waxum session id."},
			{Name: "event_name", Type: "string", Desc: "Only events with this SSE event name."},
			{Name: "max_events", Type: "integer", Desc: "Stop after this many events (default 20, max 200)."},
			{Name: "seconds", Type: "integer", Desc: "Stop after this many seconds (default 10, max 60)."},
			targetParam,
		},
		Run: func(ctx context.Context, p *upstream.Pool, a Args) (any, error) {
			max := a.Int("max_events", defaultTailEvents)
			if max <= 0 || max > maxTailEvents {
				max = defaultTailEvents
			}
			secs := a.Int("seconds", defaultTailSecs)
			if secs <= 0 || secs > maxTailSecs {
				secs = defaultTailSecs
			}
			target := a.Str("target")
			hosts, err := p.OrderedHosts(target)
			if err != nil {
				return nil, err
			}
			h := hosts[0]
			tctx, cancel := context.WithTimeout(ctx, time.Duration(secs)*time.Second)
			defer cancel()
			type ev struct {
				Type string          `json:"type"`
				ID   string          `json:"id"`
				Data json.RawMessage `json:"data"`
			}
			var collected []ev
			opts := waxum.TailOptions{SessionID: a.Str("session_id_filter"), EventName: a.Str("event_name")}
			err = h.WaxumClient().TailEvents(tctx, opts, func(e waxum.Event) error {
				collected = append(collected, ev{Type: e.Type, ID: e.ID, Data: e.Data})
				if len(collected) >= max {
					return errStopTail
				}
				return nil
			})
			if err != nil && err != errStopTail && tctx.Err() == nil {
				return nil, fmt.Errorf("host %s: %w", h.Name, err)
			}
			return map[string]any{"host": h.Name, "events": collected, "stopped_reason": stopReason(err, tctx)}, nil
		},
	}
}

var errStopTail = fmt.Errorf("waxum_events_tail: max_events reached")

func stopReason(err error, ctx context.Context) string {
	if err == errStopTail {
		return "max_events"
	}
	if ctx.Err() != nil {
		return "seconds"
	}
	return "stream_closed"
}
