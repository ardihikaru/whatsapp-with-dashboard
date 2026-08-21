package wa

import (
	"fmt"
	"log/slog"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// handleEvent is the single event handler per client (docs/04 §5). whatsmeow
// calls it from the socket loop, so it must never block: transitions are
// cheap lock work, persistence is best-effort, and the ring drops oldest.
func (s *ManagedSession) handleEvent(raw any) {
	switch evt := raw.(type) {
	case *events.Connected:
		s.setState(StateConnected)
		s.sendPresence()
	case *events.PairSuccess:
		s.mu.Lock()
		s.jid = evt.ID // pump also persists; this is defensive
		s.mu.Unlock()
	case *events.Disconnected:
		s.setState(StateDisconnected) // transient — whatsmeow auto-reconnect owns recovery
	case *events.KeepAliveTimeout:
		s.ring(fmt.Sprintf("keepalive timeout #%d (last success %s ago)",
			evt.ErrorCount, time.Since(evt.LastSuccess).Round(time.Second)))
		s.setState(StateDisconnected) // conservative: hold sends until ping/reconnect succeeds
	case *events.KeepAliveRestored:
		s.mu.Lock()
		recovering := s.state == StateDisconnected
		s.mu.Unlock()
		if recovering {
			s.setState(StateConnected) // socket recovered without a Connected event
		}
	case *events.UndecryptableMessage:
		if evt.Info.Chat != types.StatusBroadcastJID {
			s.ring(fmt.Sprintf("undecryptable message from %s in %s", evt.Info.Sender, evt.Info.Chat))
		}
	case *events.TemporaryBan:
		s.ring(fmt.Sprintf("TEMPORARY BAN: %s (expires in %s)", evt.Code, evt.Expire))
		s.permanentDown("temporary ban")
	case *events.StreamReplaced:
		s.permanentDown("stream replaced (another process using this session?)")
	case *events.LoggedOut:
		s.permanentDown(fmt.Sprintf("logged out (reason %v)", evt.Reason))
	default:
		if pd, ok := raw.(events.PermanentDisconnect); ok { // ClientOutdated, ConnectFailure…
			s.permanentDown(pd.PermanentDisconnectDescription())
		}
		// *events.Message / *events.Receipt: ring-only in MVP (extension points X1/X2)
	}
	s.ring(fmt.Sprintf("%T", raw))
}

// permanentDown is the shared terminal path: sender stopped, state
// logged_out, desired-active cleared. No auto-retry (E1: retrying a
// StreamReplaced causes takeover ping-pong).
func (s *ManagedSession) permanentDown(note string) {
	s.mu.Lock()
	s.stopSenderLocked()
	s.mu.Unlock()
	s.ring("permanent disconnect: " + note)
	s.setState(StateLoggedOut)
	_ = s.mgr.repo.SetActive(s.mgr.persistCtx, s.ID, false)
}

// sendPresence marks the device available so recipients see a push name
// instead of "-". ErrNoPushName on fresh pairs is expected (E15) — presence
// is retried on the next Connected event.
func (s *ManagedSession) sendPresence() {
	s.mu.Lock()
	cli := s.client
	s.mu.Unlock()
	if cli == nil {
		return
	}
	if err := cli.SendPresence(s.mgr.persistCtx, types.PresenceAvailable); err != nil {
		slog.Debug("send presence", "session", s.ID, "err", err)
	}
}

func (s *ManagedSession) ring(note string) {
	s.events.Append(Event{At: time.Now(), Note: note})
}
