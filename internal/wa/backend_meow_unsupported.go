package wa

import "context"

// This file holds meowBackend's stubs for the four Backend methods added
// alongside the waxum backend — media and calls. Kept out of
// backend_meow.go on purpose: that file's header comment documents it as
// "moved — not rewritten — from where it used to live inline", scoped to
// the pre-seam whatsmeow migration; these four methods never existed
// inline anywhere, so adding them there would blur that file's history.
//
// whatsmeow (go.mau.fi/whatsmeow) CAN send media in principle — it exposes
// Client.Upload/UploadReader (upload.go) — but no call site in this
// codebase has ever exercised it, and wiring it now would be designing
// ahead of need for a backend this migration is actively moving away from.
// Calls are stubbed for a stronger reason, verified against the vendored
// module rather than assumed: call.go implements only handleCallEvent and
// RejectCall — receiving and declining an incoming call — with no method to
// PLACE one, so there is no whatsmeow call to wire even if a caller needed
// one. See backend.go's ErrUnsupported doc comment for the full
// media/calls-vs-capability-interface split this stubs out one half of.
func (b *meowBackend) SendMedia(ctx context.Context, s *ManagedSession, to JID, m OutboundMedia) (string, error) {
	return "", ErrUnsupported
}

func (b *meowBackend) CallTTS(ctx context.Context, s *ManagedSession, to JID, text, voice string) (CallOutcome, error) {
	return CallOutcome{}, ErrUnsupported
}

func (b *meowBackend) CallPlay(ctx context.Context, s *ManagedSession, to JID, audioURL string) (CallOutcome, error) {
	return CallOutcome{}, ErrUnsupported
}

func (b *meowBackend) CallTerminate(ctx context.Context, s *ManagedSession, peer JID, callID, reason string) error {
	return ErrUnsupported
}
