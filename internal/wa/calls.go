package wa

import "context"

// CallOutcome is the value-copy DTO for a placed call — CallTTS/CallPlay's
// result, mirroring waxum.CallResult field-for-field for the same
// no-waxum-type-crosses-the-seam reason documented on MediaDescriptor
// (media.go).
type CallOutcome struct {
	CallID       string
	To           string
	RecordingURL string
}

// PlaceCallTTS places a voice call and speaks text via text-to-speech.
// Delegation shape mirrors JoinedGroups (group.go) exactly — see that
// method's doc comment for why the connected/client checks live in the
// backend, never here.
func (s *ManagedSession) PlaceCallTTS(ctx context.Context, to JID, text, voice string) (CallOutcome, error) {
	return s.mgr.backend.CallTTS(ctx, s, to, text, voice)
}

// PlaceCallPlay places a voice call and plays a pre-recorded audio file.
func (s *ManagedSession) PlaceCallPlay(ctx context.Context, to JID, audioURL string) (CallOutcome, error) {
	return s.mgr.backend.CallPlay(ctx, s, to, audioURL)
}

// TerminateCall ends an in-progress call.
func (s *ManagedSession) TerminateCall(ctx context.Context, peer JID, callID, reason string) error {
	return s.mgr.backend.CallTerminate(ctx, s, peer, callID, reason)
}
