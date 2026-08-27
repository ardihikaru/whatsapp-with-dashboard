package wa

import (
	"context"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/waxum"
)

// This file holds waxumBackend's media-related methods: SendMedia (a core
// Backend method — see backend.go's ErrUnsupported doc comment for why) and
// the MediaStore capability interface (UploadMedia/DownloadMedia), which
// meowBackend does not implement at all (backend_meow_unsupported.go).
//
// Every error crosses mapErr (backend_waxum.go), matching every other
// waxumBackend method in this package — a raw *waxum.APIError must never
// reach a caller outside this file's own translation.

// SendMedia is trySend's transport call for a media job (sender.go's
// sendJob.media, wired in a later phase). Delegates to waxum.SendUploaded,
// applying OutboundMedia's per-kind fields exactly as that method's own doc
// comment specifies (internal/waxum/media.go) — this method does not
// re-derive the per-kind rules, it just forwards them.
func (b *waxumBackend) SendMedia(ctx context.Context, s *ManagedSession, to JID, m OutboundMedia) (string, error) {
	res, err := b.client.SendUploaded(ctx, s.ID, waxum.MediaKind(m.Kind), to.String(),
		toWaxumDescriptor(m.Descriptor), m.Caption, m.Filename, m.PTT)
	if err != nil {
		return "", mapErr(err)
	}
	return res.MessageID, nil
}

// UploadMedia satisfies MediaStore: POSTs the file to waxum's media store
// and returns the resulting descriptor. sessionID is the plain string off
// the HTTP path (see MediaStore's doc comment for why this takes an id, not
// a *ManagedSession).
func (b *waxumBackend) UploadMedia(ctx context.Context, sessionID string, kind MediaKind, filename, mimetype string, data []byte) (MediaDescriptor, error) {
	d, err := b.client.UploadMedia(ctx, sessionID, waxum.MediaKind(kind), filename, mimetype, data)
	if err != nil {
		return MediaDescriptor{}, mapErr(err)
	}
	return fromWaxumDescriptor(*d), nil
}

// DownloadMedia satisfies MediaStore: fetches the decrypted bytes behind a
// previously issued descriptor.
func (b *waxumBackend) DownloadMedia(ctx context.Context, sessionID string, d MediaDescriptor, kind MediaKind) ([]byte, error) {
	data, err := b.client.DownloadMedia(ctx, sessionID, toWaxumDescriptor(d), waxum.MediaKind(kind))
	if err != nil {
		return nil, mapErr(err)
	}
	return data, nil
}

// toWaxumDescriptor/fromWaxumDescriptor translate across the
// no-waxum-type-crosses-the-seam boundary (MediaDescriptor's doc comment,
// media.go) — both types are field-for-field identical by design, so this
// is a plain copy, not a real mapping.
func toWaxumDescriptor(d MediaDescriptor) waxum.MediaDescriptor {
	return waxum.MediaDescriptor{
		URL:           d.URL,
		DirectPath:    d.DirectPath,
		MediaKey:      d.MediaKey,
		FileSHA256:    d.FileSHA256,
		FileEncSHA256: d.FileEncSHA256,
		FileLength:    d.FileLength,
		MediaType:     d.MediaType,
		Mimetype:      d.Mimetype,
	}
}

func fromWaxumDescriptor(d waxum.MediaDescriptor) MediaDescriptor {
	return MediaDescriptor{
		URL:           d.URL,
		DirectPath:    d.DirectPath,
		MediaKey:      d.MediaKey,
		FileSHA256:    d.FileSHA256,
		FileEncSHA256: d.FileEncSHA256,
		FileLength:    d.FileLength,
		MediaType:     d.MediaType,
		Mimetype:      d.Mimetype,
	}
}

// var _ MediaStore guards against a typo'd signature compiling and only
// failing at runtime as a bogus ErrUnsupported (see this phase's brief).
var _ MediaStore = (*waxumBackend)(nil)
