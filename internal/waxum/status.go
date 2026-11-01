package waxum

import (
	"context"
	"net/http"
)

// This file holds the three "post a WhatsApp status/story" endpoints,
// /tmp/waxum-status-api.md — live since ardihikaru/waxum:0.12.2-status-post
// but NEVER tried with a real send (see that spec's own first line). Every
// request is recipient-scoped: waxum does not fetch contacts itself, so an
// empty recipients list is rejected (400) and it is the caller's job to
// supply one — internal/wa's StatusPoster capability is the seam that
// passes a caller-built list through, unmodified, down to here.
//
// All three reuse MessageResult (models.go) as their response shape —
// waxum's own {"message_id","timestamp","to":"status@broadcast"} matches
// it field-for-field — and go through c.do like every other JSON endpoint
// in this package (no media upload involved here; the caller is expected
// to have already produced a MediaDescriptor via UploadMedia for the image/
// video variants, the same "descriptor transport" SendUploaded uses).

// PostStatusText posts a text status with an optional background colour.
// backgroundARGB is omitted from the request entirely when 0 — waxum's own
// documented default (0xFF1E6E4F) applies server-side in that case; sending
// a literal 0 would ask for a transparent/black background instead of
// deferring to that default.
func (c *Client) PostStatusText(ctx context.Context, id, text string, backgroundARGB uint32, recipients []string) (*MessageResult, error) {
	body := map[string]any{
		"text":       text,
		"recipients": recipients,
	}
	if backgroundARGB != 0 {
		body["background_argb"] = backgroundARGB
	}
	var out MessageResult
	if err := c.do(ctx, http.MethodPost, sessionPath(id, "/status/text"), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// PostStatusImage posts an image status built from a previously-uploaded
// MediaDescriptor. caption is omitted when empty, the same rule
// SendUploaded's image/video case follows.
func (c *Client) PostStatusImage(ctx context.Context, id string, d MediaDescriptor, caption string, recipients []string) (*MessageResult, error) {
	return c.postStatusMedia(ctx, id, "/status/image", d, caption, recipients)
}

// PostStatusVideo posts a video status built from a previously-uploaded
// MediaDescriptor. caption is omitted when empty, the same rule
// SendUploaded's image/video case follows.
func (c *Client) PostStatusVideo(ctx context.Context, id string, d MediaDescriptor, caption string, recipients []string) (*MessageResult, error) {
	return c.postStatusMedia(ctx, id, "/status/video", d, caption, recipients)
}

func (c *Client) postStatusMedia(ctx context.Context, id, path string, d MediaDescriptor, caption string, recipients []string) (*MessageResult, error) {
	media := map[string]any{
		"url":             d.URL,
		"direct_path":     d.DirectPath,
		"media_key":       d.MediaKey,
		"file_sha256":     d.FileSHA256,
		"file_enc_sha256": d.FileEncSHA256,
		"file_length":     d.FileLength,
		"mimetype":        d.Mimetype,
	}
	body := map[string]any{
		"media":      media,
		"recipients": recipients,
	}
	if caption != "" {
		body["caption"] = caption
	}
	var out MessageResult
	if err := c.do(ctx, http.MethodPost, sessionPath(id, path), body, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}
