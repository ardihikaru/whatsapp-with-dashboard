package wa

import "context"

// This file holds waxumBackend's implementation of Backend's three call
// methods (CallTTS/CallPlay/CallTerminate) — see calls.go's CallOutcome and
// ManagedSession wrappers, and backend.go's ErrUnsupported doc comment for
// why these are core Backend methods rather than an optional capability.
// Being core Backend methods, a wrong signature here already fails to
// compile at NewWaxumBackend's return statement (backend_waxum.go) —
// unlike the capability interfaces (MediaStore/SessionOps/WebhookAdmin),
// there is no separate var _ assertion needed for these.

func (b *waxumBackend) CallTTS(ctx context.Context, s *ManagedSession, to JID, text, voice string) (CallOutcome, error) {
	res, err := b.client.CallTTS(ctx, s.ID, to.String(), text, voice)
	if err != nil {
		return CallOutcome{}, mapErr(err)
	}
	return CallOutcome{CallID: res.CallID, To: res.To, RecordingURL: res.RecordingURL}, nil
}

func (b *waxumBackend) CallPlay(ctx context.Context, s *ManagedSession, to JID, audioURL string) (CallOutcome, error) {
	res, err := b.client.CallPlay(ctx, s.ID, to.String(), audioURL)
	if err != nil {
		return CallOutcome{}, mapErr(err)
	}
	return CallOutcome{CallID: res.CallID, To: res.To, RecordingURL: res.RecordingURL}, nil
}

func (b *waxumBackend) CallTerminate(ctx context.Context, s *ManagedSession, peer JID, callID, reason string) error {
	return mapErr(b.client.Terminate(ctx, s.ID, peer.String(), callID, reason))
}
