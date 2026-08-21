package wa

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
)

// StartPairing provisions a fresh in-memory device and starts the QR pump
// (docs/05 §2). Only new/logged_out sessions may pair; the losing side of a
// double-click sees StatePairing and gets ErrWrongState (→ 409).
func (s *ManagedSession) StartPairing(mode PairMode) error {
	s.mu.Lock()
	if s.state != StateNew && s.state != StateLoggedOut {
		s.mu.Unlock()
		return ErrWrongState
	}
	if s.client != nil { // stale client must be gone before a fresh NewDevice
		s.mu.Unlock()
		return ErrWrongState
	}
	dev := s.mgr.container.NewDevice() // in-memory; persisted only on successful pair
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
	s.ring("pairing failed: " + cause.Error())
}
