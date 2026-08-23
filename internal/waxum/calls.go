package waxum

import (
	"context"
	"net/http"
)

// This file holds waxum's VOICE-CALL endpoints. There are three: tts, play,
// terminate. There is deliberately NOT a fourth for waxum's "ring" call
// endpoint (under /calls/, same family as the three above) — it transmits
// silence, not real audio, so a Ring method here would let an operator
// believe they placed a call when nothing was said. TestNoRingCallMethodExists
// asserts the literal path is unreachable from this package's own source,
// not just "we didn't happen to write it" — see that test for why even this
// comment has to avoid spelling the path out contiguously.

// CallTTS places a call and speaks text via text-to-speech. voice defaults
// to DefaultTTSVoice ("id-ID-ArdiNeural") when empty.
func (c *Client) CallTTS(ctx context.Context, id, to, text, voice string) (*CallResult, error) {
	if voice == "" {
		voice = DefaultTTSVoice
	}
	body := map[string]string{"to": to, "text": text, "voice": voice}
	var out CallResult
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/calls/tts"), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// CallPlay places a call and plays a pre-recorded audio file from a URL.
//
// Body shape verified against waxum's src/models/calls.rs
// (PlayCallRequest): {"to":…,"audio_url":…}. That struct also carries
// answer_grace_ms and record, both Option — neither is sent here, so
// waxum's own server-side defaults for them apply, same as Terminate's
// reason (see its doc comment).
func (c *Client) CallPlay(ctx context.Context, id, to, audioURL string) (*CallResult, error) {
	body := map[string]string{"to": to, "audio_url": audioURL}
	var out CallResult
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/calls/play"), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// Terminate ends an in-progress call.
//
// Body shape verified against waxum's src/models/calls.rs
// (TerminateCallRequest): {"peer":…,"call_id":…,"reason":…}, where reason
// is Option<String> and defaults server-side to "hangup". reason is
// therefore OMITTED from the body when empty rather than sent as "" — an
// explicit empty string would override that default instead of letting it
// apply. See TestTerminateOmitsTheReasonWhenEmptySoWaxumAppliesItsDefault.
func (c *Client) Terminate(ctx context.Context, id, peer, callID, reason string) error {
	body := map[string]any{"peer": peer, "call_id": callID}
	if reason != "" {
		body["reason"] = reason
	}
	return c.do(ctx, http.MethodPost, sessionPath(id, "/calls/terminate"), body, nil, true)
}
