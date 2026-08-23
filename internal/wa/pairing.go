package wa

import (
	"sync"
	"time"
)

// PairMode selects how the operator links the device.
type PairMode string

const (
	PairModeQR    PairMode = "qr"
	PairModePhone PairMode = "phone"
)

// PairingState is the box between the pairing pump (only writer) and HTTP
// handlers (snapshot readers). It outlives the pump on failure so the status
// partial can show the terminal error; StartPairing replaces it.
type PairingState struct {
	mu        sync.Mutex
	mode      PairMode
	code      string    // current raw QR payload ("2@…") — the string IS the QR content
	gen       int       // incremented per code; cache-buster for <img src="qr.png?g=N">
	expiresAt time.Time // now + item.Timeout; drives the server-rendered countdown
	phoneCode string    // "XXXX-XXXX" once PairPhone returns (mode=phone)
	phoneErr  error
	done      bool // terminal item consumed
	err       error
}

// PairingSnapshot is the value copy handlers render from.
type PairingSnapshot struct {
	Mode        PairMode
	Gen         int
	HasCode     bool
	SecondsLeft int
	PhoneCode   string
	PhoneErr    string
	Done        bool
	Err         string
}

// SetCode installs a rotated QR payload and bumps the generation counter.
func (p *PairingState) SetCode(code string, ttl time.Duration) {
	p.mu.Lock()
	p.code, p.gen, p.expiresAt = code, p.gen+1, time.Now().Add(ttl)
	p.mu.Unlock()
}

// SetPhoneCode records the PairPhone result (code or error).
func (p *PairingState) SetPhoneCode(code string, err error) {
	p.mu.Lock()
	p.phoneCode, p.phoneErr = code, err
	p.mu.Unlock()
}

// finish marks the terminal outcome; nil err means success.
func (p *PairingState) finish(err error) {
	p.mu.Lock()
	p.done, p.err = true, err
	p.mu.Unlock()
}

// Snapshot copies the state for rendering.
func (p *PairingState) Snapshot() PairingSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	snap := PairingSnapshot{
		Mode: p.mode, Gen: p.gen, PhoneCode: p.phoneCode, Done: p.done,
		HasCode: p.code != "" && time.Now().Before(p.expiresAt),
	}
	if left := time.Until(p.expiresAt); p.code != "" && left > 0 {
		snap.SecondsLeft = int(left.Seconds())
	}
	if p.err != nil {
		snap.Err = p.err.Error()
	}
	if p.phoneErr != nil {
		snap.PhoneErr = p.phoneErr.Error()
	}
	return snap
}

// CurrentCode returns the raw QR payload if one is live (not expired).
func (p *PairingState) CurrentCode() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.code, p.code != "" && time.Now().Before(p.expiresAt) && !p.done
}

// PairingCode is the nil-safe session accessor for the qr.png endpoint.
func (s *ManagedSession) PairingCode() (string, bool) {
	s.mu.Lock()
	ps := s.pairing
	s.mu.Unlock()
	if ps == nil {
		return "", false
	}
	return ps.CurrentCode()
}

// PairingSnapshot is the nil-safe session accessor for the status partial.
func (s *ManagedSession) PairingSnapshot() (PairingSnapshot, bool) {
	s.mu.Lock()
	ps := s.pairing
	s.mu.Unlock()
	if ps == nil {
		return PairingSnapshot{}, false
	}
	return ps.Snapshot(), true
}

// StartPairing provisions and starts pairing through the session's backend
// (see backend_meow.go for the whatsmeow implementation, moved unchanged
// from what used to be this method's entire body). rootCtx (not a per-call
// ctx) matches every other backend-driven background loop this package
// starts — StartAll's boot reconnects use the same context.
func (s *ManagedSession) StartPairing(mode PairMode) error {
	return s.mgr.backend.StartPairing(s.mgr.rootCtx, s, mode)
}
