package wa

import "context"

// This file holds Manager's accessors for the optional capability
// interfaces (capabilities.go). Each does the SAME nil-guard Snapshot
// (session.go) already does for m.backend — "mgr.backend is nil only in
// tests that never activate a session; guard rather than require every stub
// to provide one" — and then a type assertion against Backend, returning
// ErrUnsupported on either failure. That assertion IS "wired to whichever
// backend lacks it" for gateway administration, the same way meowBackend's
// stubs (backend_meow_unsupported.go) are for media/calls — see
// backend.go's ErrUnsupported doc comment for the full split.
//
// m.backend is set once, in NewManager, and never reassigned afterwards
// (grep confirms no other write site) — reading it here with no lock held
// is the same assumption Snapshot already makes.

// GatewayHealth asks the backend for a fleet-health probe, if it has one.
func (m *Manager) GatewayHealth(ctx context.Context) error {
	if m.backend == nil {
		return ErrUnsupported
	}
	gh, ok := m.backend.(GatewayHealth)
	if !ok {
		return ErrUnsupported
	}
	return gh.Health(ctx)
}

// CachedBackendHealth returns the backend's name and its last CACHED
// reachability verdict (GatewayHealth.Reachable) — never calls the backend;
// see that method's doc comment for why. supported is false when the
// backend implements no GatewayHealth at all (meowBackend, or a nil
// backend), in which case name/reachable/known are meaningless. Used by
// the unauthenticated GET /api/v1/health container healthcheck (api.go's
// GetHealth) — see GatewayHealth.Reachable's doc comment.
func (m *Manager) CachedBackendHealth() (name string, reachable, known, supported bool) {
	if m.backend == nil {
		return "", false, false, false
	}
	gh, ok := m.backend.(GatewayHealth)
	if !ok {
		return m.backend.Name(), false, false, false
	}
	reachable, known = gh.Reachable()
	return m.backend.Name(), reachable, known, true
}

// MediaStore returns the backend's media upload/download store, if it has
// one.
func (m *Manager) MediaStore() (MediaStore, error) {
	if m.backend == nil {
		return nil, ErrUnsupported
	}
	ms, ok := m.backend.(MediaStore)
	if !ok {
		return nil, ErrUnsupported
	}
	return ms, nil
}

// SessionOps returns the backend's session-administration operations, if
// it has any.
func (m *Manager) SessionOps() (SessionOps, error) {
	if m.backend == nil {
		return nil, ErrUnsupported
	}
	ops, ok := m.backend.(SessionOps)
	if !ok {
		return nil, ErrUnsupported
	}
	return ops, nil
}

// ContactDirectory returns the backend's contact-verification operations,
// if it has any.
func (m *Manager) ContactDirectory() (ContactDirectory, error) {
	if m.backend == nil {
		return nil, ErrUnsupported
	}
	cd, ok := m.backend.(ContactDirectory)
	if !ok {
		return nil, ErrUnsupported
	}
	return cd, nil
}

// WebhookAdmin returns the backend's webhook management operations, if it
// has any.
func (m *Manager) WebhookAdmin() (WebhookAdmin, error) {
	if m.backend == nil {
		return nil, ErrUnsupported
	}
	wh, ok := m.backend.(WebhookAdmin)
	if !ok {
		return nil, ErrUnsupported
	}
	return wh, nil
}

// MessageHistory returns the backend's customer-reply-reading operations,
// if it has any. inbound.go's pump calls this once per tick, per session,
// to check whether the current backend supports ingest at all before
// polling — the whatsmeow backend never does (meowBackend implements no
// capability interface — TestTheWhatsmeowBackendSatisfiesNoCapabilityInterface),
// so this is how the pump stays a no-op there without a backend-name
// switch.
func (m *Manager) MessageHistory() (MessageHistory, error) {
	if m.backend == nil {
		return nil, ErrUnsupported
	}
	mh, ok := m.backend.(MessageHistory)
	if !ok {
		return nil, ErrUnsupported
	}
	return mh, nil
}
