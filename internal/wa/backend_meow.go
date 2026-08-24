package wa

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// This file holds the whatsmeow implementation of Backend, moved — not
// rewritten — from where it used to live inline in ManagedSession/Manager:
//   - attachClientLocked/detachClientLocked: session.go
//   - the whatsmeow half of Activate/Deactivate: session.go
//   - handleEvent/sendPresence: events.go
//   - StartPairing/pairPump/onPaired/onPairFailed: pairing_pump.go
//   - trySend's cli.SendMessage call: sender.go
//   - JoinedGroups's cli.GetJoinedGroups call: group.go
//   - the sqlstore.Container uses (NewDevice/GetDevice/DeleteDevice) and
//     newWhatsAppLogger: manager_crud.go, session.go, pairing_pump.go,
//     logger.go
// Behaviour is unchanged; only the file (and, where noted, the receiver —
// the Backend interface methods) moved.

// meowBackend is the whatsmeow Backend. It owns the sqlite crypto store
// (sqlstore.Container); every other piece of whatsmeow-specific state lives
// on ManagedSession itself (client, jid — see backend.go's design note).
type meowBackend struct {
	container *sqlstore.Container
}

// NewWhatsmeowBackend opens the whatsmeow sqlite crypto store on db and
// upgrades it to the latest schema. db is the app's dedicated whatsmeow
// SQLite handle (internal/db.Open), not the app-data store.
func NewWhatsmeowBackend(ctx context.Context, db *sql.DB, dialect string) (Backend, error) {
	container := sqlstore.NewWithDB(db, dialect, waLog.Stdout("sqlstore", "INFO", true))
	if err := container.Upgrade(ctx); err != nil { // NewWithDB does NOT auto-upgrade
		return nil, err
	}
	return &meowBackend{container: container}, nil
}

func (b *meowBackend) Name() string { return "whatsmeow" }

func (b *meowBackend) Close() {
	// The underlying *sql.DB is owned and closed by main.go (internal/db.Open
	// is deferred there); nothing backend-level needs releasing on top of
	// that today. Kept as a real (empty) method rather than omitted so the
	// waxum backend's Close — which DOES need to stop a poll loop — is not
	// the odd one out.
}

// Live wraps Client.IsConnected(), an in-process, non-blocking check —
// exactly what Snapshot did inline before this seam existed. See Backend.Live
// for why this must stay I/O-free.
func (b *meowBackend) Live(s *ManagedSession) bool {
	return s.client != nil && s.client.IsConnected()
}

// attachClientLocked enforces the one-Client-per-Device invariant (hard rule
// 3). Called under s.mu; dev comes from container.GetDevice or NewDevice.
func (s *ManagedSession) attachClientLocked(dev *store.Device) (*whatsmeow.Client, error) {
	if s.client != nil {
		return nil, fmt.Errorf("session %s already has a client", s.ID)
	}
	cli := whatsmeow.NewClient(dev, newWhatsAppLogger(s.ID))
	cli.AddEventHandler(s.handleEvent) // registered once, never removed
	cli.AutoReconnectHook = func(error) bool {
		s.mu.Lock()
		dead := s.state == StateLoggedOut || s.client != cli
		s.mu.Unlock()
		return !dead // stop retrying once this client is discarded or logged out
	}
	s.ensureCtxLocked() // transport-agnostic; see session.go
	s.client = cli
	return cli, nil
}

// detachClientLocked discards the client whole (the handler stays registered
// on the dead client — we never call RemoveEventHandler). Called under s.mu.
func (s *ManagedSession) detachClientLocked() {
	if s.cancel != nil {
		s.cancel()
	}
	s.client, s.ctx, s.cancel = nil, nil, nil
}

// Connect is the whatsmeow half of what used to be inline in Activate: reuse
// or attach a client, start the sender, dial. Callers hold NO lock.
func (b *meowBackend) Connect(ctx context.Context, s *ManagedSession) error {
	s.mu.Lock()
	cli := s.client
	if cli == nil {
		dev, err := b.container.GetDevice(ctx, s.jid)
		if err != nil || dev == nil {
			s.mu.Unlock()
			s.markDeviceGone()
			return ErrDeviceGone
		}
		if cli, err = s.attachClientLocked(dev); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.startSenderLocked()
	sctx := s.ctx
	s.mu.Unlock()

	if err := cli.ConnectContext(sctx); err != nil { // I/O unlocked; Connected event → setState
		s.setState(StateDisconnected) // whatsmeow auto-reconnect keeps trying
		return err
	}
	return nil
}

// Disconnect is the whatsmeow half of what used to be inline in Deactivate.
func (b *meowBackend) Disconnect(ctx context.Context, s *ManagedSession) error {
	s.mu.Lock()
	cli := s.client
	s.mu.Unlock()
	if cli != nil {
		cli.Disconnect()
	}
	return nil
}

// Logout is the whatsmeow half of what used to be inline in Manager.Delete:
// revoke the paired device's credentials, or just discard a never-paired
// client. Errors are logged, not returned — the caller (Manager.Delete) must
// proceed to the repo delete regardless, matching the pre-seam behaviour.
func (b *meowBackend) Logout(ctx context.Context, s *ManagedSession) error {
	s.mu.Lock()
	cli := s.client
	s.client = nil
	paired := cli != nil && cli.Store.ID != nil
	s.mu.Unlock()
	if cli == nil {
		return nil
	}
	if !paired {
		cli.Disconnect()
		return nil
	}
	if err := cli.Logout(ctx); err != nil {
		slog.Warn("logout failed; forcing local cleanup", "session", s.ID, "err", err)
		cli.Disconnect()
		if err := b.container.DeleteDevice(ctx, cli.Store); err != nil {
			slog.Warn("device purge failed", "session", s.ID, "err", err)
		}
	}
	return nil
}

// SendText is trySend's cli.SendMessage call. ctx already carries the 30s
// deadline sender.go applies before calling this.
func (b *meowBackend) SendText(ctx context.Context, s *ManagedSession, to JID, text string) (string, error) {
	s.mu.Lock()
	cli := s.client
	s.mu.Unlock()
	if cli == nil {
		return "", ErrNotConnected
	}
	resp, err := cli.SendMessage(ctx, types.JID{User: to.User, Server: to.Server}, &waE2E.Message{
		Conversation: proto.String(text), // plain text, \n preserved — MVP message shape
	})
	if err != nil {
		return "", err
	}
	return string(resp.ID), nil
}

// JoinedGroups is the whatsmeow half of what used to be the entire
// ManagedSession.JoinedGroups body.
//
// The lock is taken to read s.client and RELEASED before the network call —
// the rule the rest of this package follows. Holding s.mu across a whatsmeow
// round trip would let one slow group fetch block Snapshot, and therefore
// the whole sessions page, for as long as WhatsApp felt like taking.
func (b *meowBackend) JoinedGroups(ctx context.Context, s *ManagedSession) ([]GroupInfo, error) {
	s.mu.Lock()
	cli := s.client
	connected := s.state == StateConnected && cli != nil
	s.mu.Unlock()
	if !connected {
		return nil, ErrNotConnected
	}

	groups, err := cli.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]GroupInfo, 0, len(groups))
	for _, g := range groups {
		out = append(out, GroupInfo{
			JID:          g.JID.String(),
			Name:         g.GroupName.Name,
			Participants: len(g.Participants),
		})
	}
	return out, nil
}

// StartPairing provisions a fresh in-memory device and starts the QR pump
// (docs/05 §2). Only new/logged_out sessions may pair; the losing side of a
// double-click sees StatePairing and gets ErrWrongState (→ 409).
//
// ctx is accepted for symmetry with the rest of Backend; the pairing pump's
// own lifetime is governed by s.ctx (established via ensureCtxLocked), same
// as pre-seam.
func (b *meowBackend) StartPairing(ctx context.Context, s *ManagedSession, mode PairMode) error {
	s.mu.Lock()
	if s.state != StateNew && s.state != StateLoggedOut {
		s.mu.Unlock()
		return ErrWrongState
	}
	if s.client != nil { // stale client must be gone before a fresh NewDevice
		s.mu.Unlock()
		return ErrWrongState
	}
	dev := b.container.NewDevice() // in-memory; persisted only on successful pair
	cli, err := s.attachClientLocked(dev)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	sctx := s.ctx
	// GetQRChannel must run BEFORE Connect and while Store.ID == nil — both
	// guaranteed here (fresh NewDevice, client untouched until we unlock).
	qrCh, err := cli.GetQRChannel(sctx)
	if err != nil {
		s.detachClientLocked()
		s.mu.Unlock()
		return err
	}
	ps := &PairingState{mode: mode}
	s.pairing, s.state = ps, StatePairing
	s.mu.Unlock()

	if err := cli.Connect(); err != nil { // dial kickoff, unlocked (network I/O)
		s.mu.Lock()
		s.pairing = nil
		s.detachClientLocked()
		s.state = StateNew
		s.mu.Unlock()
		return err
	}
	_ = s.mgr.repo.UpdateStatus(s.mgr.persistCtx, s.ID, string(StatePairing))
	s.mgr.go1(func() { s.pairPump(sctx, cli, qrCh, ps, mode) })
	return nil
}

// pairPump consumes the QR channel until its single terminal item. The
// channel emits ~6 rotating codes (~60s first, ~20s after) then exactly one
// of success/timeout/error and closes itself.
func (s *ManagedSession) pairPump(ctx context.Context, cli *whatsmeow.Client,
	qrCh <-chan whatsmeow.QRChannelItem, ps *PairingState, mode PairMode) {

	first := true
	for item := range qrCh {
		switch {
		case item.Event == whatsmeow.QRChannelEventCode:
			ps.SetCode(item.Code, item.Timeout)
			if first && mode == PairModePhone {
				// Requires an established connection — guaranteed once the
				// first QR item arrives. QR stays valid underneath.
				code, err := cli.PairPhone(ctx, s.Phone, true,
					whatsmeow.PairClientChrome, "Chrome (Linux)")
				ps.SetPhoneCode(code, err)
			}
			first = false
		case item.Event == "success":
			s.onPaired(cli)
			return
		case item.Event == "timeout":
			s.onPairFailed(cli, errors.New("pairing window expired (~160s) — restart to try again"))
			return
		case item.Event == whatsmeow.QRChannelEventError:
			s.onPairFailed(cli, item.Error)
			return
		default:
			if strings.HasPrefix(item.Event, "err-") { // scanned-without-multidevice etc.
				s.onPairFailed(cli, fmt.Errorf("pairing failed: %s", item.Event))
				return
			}
			// passkey-request / passkey-confirmation: informational, keep pumping (E13)
		}
	}
	// Channel closed without a terminal item: ctx canceled (shutdown/delete).
	err := ctx.Err()
	if err == nil {
		err = context.Canceled
	}
	s.onPairFailed(cli, err)
}

// onPaired commits a successful pair: jid + active=1 persisted, sender
// started. events.Connected fires after WhatsApp's post-pair reconnect and
// re-confirms the state + presence.
func (s *ManagedSession) onPaired(cli *whatsmeow.Client) {
	jid := *cli.Store.ID // set by whatsmeow before "success" is delivered
	s.mu.Lock()
	s.jid = jid
	if s.pairing != nil {
		s.pairing.finish(nil)
		s.pairing = nil // status partial derives success from state==connected
	}
	s.startSenderLocked()
	s.active = true // mirrors SetPaired's active=1 below — see reconnect.go's eligibility check
	s.mu.Unlock()
	_ = s.mgr.repo.SetPaired(s.mgr.persistCtx, s.ID, jid.String())
	s.setState(StateConnected)
	s.ring("paired as " + jid.String())
}

// onPairFailed rolls back to new. The whatsmeow store has no device row
// (Device is saved only on success), so nothing needs purging. The pairing
// box is kept (done + err) so the status partial can show why.
func (s *ManagedSession) onPairFailed(cli *whatsmeow.Client, cause error) {
	if cause == nil {
		cause = errors.New("pairing aborted")
	}
	s.mu.Lock()
	if s.pairing != nil {
		s.pairing.finish(cause)
	}
	s.detachClientLocked()
	s.state = StateNew
	s.mu.Unlock()
	cli.Disconnect()
	_ = s.mgr.repo.UpdateStatus(s.mgr.persistCtx, s.ID, string(StateNew))
	s.ringLevel("warn", "pairing failed: "+cause.Error())
}

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
		s.ringLevel("warn", fmt.Sprintf("keepalive timeout #%d (last success %s ago)",
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
		s.ringLevel("warn", fmt.Sprintf("TEMPORARY BAN: %s (expires in %s)", evt.Code, evt.Expire))
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
	s.active = false // terminal: never auto-reconnected — see reconnect.go's eligibility check
	s.mu.Unlock()
	s.ringLevel("warn", "permanent disconnect: "+note)
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

// ring records note at level "info" — see ringLevel.
func (s *ManagedSession) ring(note string) {
	s.ringLevel("info", note)
}

// ringLevel is the one choke point every session-scoped diagnostic event
// goes through: it appends to s's OWN per-session ring (Snapshot.LastEvents
// — unchanged behaviour) AND, in the same call, to Manager's app-wide ring
// (Manager.RecentEvents, internal/handler's system log page — see
// manager.go's sysRing doc comment). This is deliberately the ONLY place
// that fans an event out to both, so every existing s.ring(...) call site
// (state transitions in setState, pairing outcomes in
// onPaired/onPairFailed/backend_waxum.go's finishPairingSuccess, status-poll
// failures in statusPump, reconnect attempts and the stalled decision in
// reconnect.go, send failures in sender.go's finishJob) reaches the system
// log automatically, with no per-call-site wiring to forget. Both Appends
// share one Event value (and so one timestamp) rather than being
// constructed twice.
func (s *ManagedSession) ringLevel(level, note string) {
	e := Event{At: time.Now(), Note: note, SessionID: s.ID, SessionName: s.Name, Level: level}
	s.events.Append(e)
	if s.mgr != nil { // mgr is always set in production; guarded for any hand-built test session
		s.mgr.sysRing.Append(e)
	}
}

// sessionLogger keeps whatsmeow's operational logs visible while demoting
// self-healing Signal fanout noise that does not make SendMessage fail.
type sessionLogger struct {
	inner waLog.Logger
}

func newWhatsAppLogger(sessionID string) waLog.Logger {
	return sessionLogger{inner: waLog.Stdout("wa-"+shortSessionID(sessionID), "INFO", true)}
}

func (l sessionLogger) Errorf(msg string, args ...any) { l.inner.Errorf(msg, args...) }
func (l sessionLogger) Infof(msg string, args ...any)  { l.inner.Infof(msg, args...) }
func (l sessionLogger) Debugf(msg string, args ...any) { l.inner.Debugf(msg, args...) }

func (l sessionLogger) Warnf(msg string, args ...any) {
	if isRecoverableSignalFanoutWarning(msg, args...) {
		l.inner.Debugf("%s", fmt.Sprintf(msg, args...))
		return
	}
	l.inner.Warnf(msg, args...)
}

func (l sessionLogger) Sub(module string) waLog.Logger {
	return sessionLogger{inner: l.inner.Sub(module)}
}

func isRecoverableSignalFanoutWarning(msg string, args ...any) bool {
	if strings.HasPrefix(msg, "Failed to encrypt ") {
		return true
	}
	if strings.Contains(msg, "clearing stored identity and retrying") {
		return true
	}
	return false
}

func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
