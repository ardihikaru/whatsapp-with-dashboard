package wa

import (
	"context"
)

// This file holds waxumBackend's implementation of the StatusPoster
// capability interface (capabilities.go) — see mapErr (backend_waxum.go)
// for how waxum's 404/503/429 map onto ErrNotFound/ErrNotConnected/
// ErrBackendBusy, the same translation every other waxumBackend method in
// this package goes through.

// PostStatusText satisfies StatusPoster.
func (b *waxumBackend) PostStatusText(ctx context.Context, sessionID, text string, backgroundARGB uint32, recipients []string) (string, error) {
	res, err := b.client.PostStatusText(ctx, sessionID, text, backgroundARGB, recipients)
	if err != nil {
		return "", mapErr(err)
	}
	return res.MessageID, nil
}

// PostStatusImage satisfies StatusPoster, going through the SAME
// upload-then-describe path SendMedia uses (toWaxumDescriptor,
// backend_waxum_media.go) — waxum's SSRF guard refuses a bare URL, the same
// reason SendMedia itself cannot be used for anything WSM hosts internally.
func (b *waxumBackend) PostStatusImage(ctx context.Context, sessionID string, d MediaDescriptor, caption string, recipients []string) (string, error) {
	res, err := b.client.PostStatusImage(ctx, sessionID, toWaxumDescriptor(d), caption, recipients)
	if err != nil {
		return "", mapErr(err)
	}
	return res.MessageID, nil
}

// PostStatusVideo satisfies StatusPoster — see PostStatusImage's doc comment.
func (b *waxumBackend) PostStatusVideo(ctx context.Context, sessionID string, d MediaDescriptor, caption string, recipients []string) (string, error) {
	res, err := b.client.PostStatusVideo(ctx, sessionID, toWaxumDescriptor(d), caption, recipients)
	if err != nil {
		return "", mapErr(err)
	}
	return res.MessageID, nil
}

// var _ StatusPoster guards against a typo'd signature compiling and only
// failing at runtime as a bogus ErrUnsupported.
var _ StatusPoster = (*waxumBackend)(nil)
