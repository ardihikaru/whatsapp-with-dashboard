package waxum

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
)

// maxMediaResponseBytes overrides the package's default maxResponseBytes
// (4 MiB) for DownloadMedia only. waxum returns the file's bytes
// base64-encoded inside a JSON envelope, which inflates size by roughly a
// third — a 16 MiB file base64s to ~21 MiB, so the ordinary JSON cap would
// silently truncate it well below waxum's own 100 MiB upload ceiling. See
// TestDownloadMediaAcceptsAResponseLargerThanTheJSONResponseCap.
const maxMediaResponseBytes = 32 << 20 // 32 MiB

// MediaDescriptor is waxum's "uploaded media" handle: the response of
// UploadMedia, the input to DownloadMedia, and (nested under the target
// kind's own key) the payload SendUploaded posts to actually deliver the
// message. It intentionally mirrors waxum's wire shape field-for-field so no
// translation is needed between the three calls.
type MediaDescriptor struct {
	URL           string `json:"url"`
	DirectPath    string `json:"direct_path"`
	MediaKey      string `json:"media_key"`
	FileSHA256    string `json:"file_sha256"`
	FileEncSHA256 string `json:"file_enc_sha256"`
	FileLength    int64  `json:"file_length"`
	MediaType     string `json:"media_type"`
	Mimetype      string `json:"mimetype"`
}

// UploadMedia POSTs a file to waxum's media store as multipart/form-data and
// returns the resulting descriptor. The upload itself does not send anything
// to WhatsApp — SendUploaded does that, given this descriptor.
//
// Fields, per waxum's upload handler: "file" (the bytes, carrying filename
// and a per-part Content-Type of mimetype), "media_type" (kind's own string,
// e.g. "image"), "mimetype".
func (c *Client) UploadMedia(ctx context.Context, id string, kind MediaKind, filename, mimetype string, data []byte) (*MediaDescriptor, error) {
	body, contentType, err := buildMediaUploadBody(kind, filename, mimetype, data)
	if err != nil {
		return nil, fmt.Errorf("waxum: building media upload body: %w", err)
	}
	var out MediaDescriptor
	if err := c.send(ctx, http.MethodPost, sessionPath(id, "/media/upload"), contentType, body, &out, true, c.maxResponseBytes); err != nil {
		return nil, err
	}
	return &out, nil
}

// buildMediaUploadBody fully buffers a multipart/form-data body into memory
// (rather than streaming) so that a 429 retry inside send() can replay the
// SAME body on the next attempt — a streamed multipart.Writer backed by an
// io.Pipe would be spent after one read, the exact bug the doc comment on
// send()'s body parameter (and transport.go's) exists to prevent. Media
// files are bounded well below what makes full buffering a problem (see
// WSM_WAXUM_UPLOAD_LIMIT in the phase that wires this to an HTTP handler).
func buildMediaUploadBody(kind MediaKind, filename, mimetype string, data []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	if mimetype != "" {
		header.Set("Content-Type", mimetype)
	}
	part, err := w.CreatePart(header)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("media_type", string(kind)); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("mimetype", mimetype); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// DownloadMedia fetches the decrypted bytes behind a descriptor previously
// returned by UploadMedia (or read off an incoming message). waxum answers
// with the file base64-encoded inside a JSON envelope
// ({"data":"<base64>","size":N}) — this uses maxMediaResponseBytes, not the
// package's ordinary 4 MiB response cap, precisely because of that
// inflation; see that const's doc comment.
func (c *Client) DownloadMedia(ctx context.Context, id string, d MediaDescriptor, kind MediaKind) ([]byte, error) {
	body := map[string]any{
		"direct_path":     d.DirectPath,
		"media_key":       d.MediaKey,
		"file_sha256":     d.FileSHA256,
		"file_enc_sha256": d.FileEncSHA256,
		"file_length":     d.FileLength,
		"media_type":      string(kind),
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("waxum: encoding media download request: %w", err)
	}
	var out struct {
		Data string `json:"data"`
		Size int64  `json:"size"`
	}
	if err := c.send(ctx, http.MethodPost, sessionPath(id, "/media/download"), "application/json", encoded, &out, true, maxMediaResponseBytes); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return nil, fmt.Errorf("waxum: decoding base64 media data: %w", err)
	}
	return data, nil
}

// SendUploaded delivers a message built from a descriptor previously
// returned by UploadMedia — the "descriptor" transport, as opposed to
// SendMedia's "URL" transport (see that method's doc comment for why the URL
// form cannot reach anything WSM hosts internally).
//
// The descriptor is nested as an OBJECT under the kind's own key, the same
// trap SendMedia's doc comment (and
// TestSendImageWrapsTheURLInAnObjectUnderTheImageKey) already guards for the
// URL form — getting this wrong produces a 422.
//
// Per-kind rules, verified against waxum's src/models/messages.rs:
//   - image, video: caption is sent only when non-empty; no filename, no ptt.
//   - document: filename is REQUIRED and non-optional — always sent, even
//     empty; caption is optional.
//   - audio: ptt is always sent; caption is NEVER sent — waxum's
//     SendAudioRequest has no caption field at all, so sending one anyway
//     would be silently ignored at best and is worth never doing.
func (c *Client) SendUploaded(ctx context.Context, id string, kind MediaKind, to string, d MediaDescriptor, caption, filename string, ptt bool) (*MessageResult, error) {
	switch kind {
	case MediaImage, MediaVideo, MediaAudio, MediaDocument:
	default:
		return nil, fmt.Errorf("waxum: unknown media kind %q", kind)
	}

	descriptor := map[string]any{
		"url":             d.URL,
		"direct_path":     d.DirectPath,
		"media_key":       d.MediaKey,
		"file_sha256":     d.FileSHA256,
		"file_enc_sha256": d.FileEncSHA256,
		"file_length":     d.FileLength,
		"mimetype":        d.Mimetype,
	}

	body := map[string]any{
		"to":         to,
		string(kind): descriptor,
	}

	switch kind {
	case MediaImage, MediaVideo:
		if caption != "" {
			body["caption"] = caption
		}
	case MediaDocument:
		body["filename"] = filename
		if caption != "" {
			body["caption"] = caption
		}
	case MediaAudio:
		body["ptt"] = ptt
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("waxum: encoding send-uploaded request: %w", err)
	}
	var out MessageResult
	if err := c.send(ctx, http.MethodPost, sessionPath(id, "/messages/"+string(kind)), "application/json", encoded, &out, true, c.maxResponseBytes); err != nil {
		return nil, err
	}
	return &out, nil
}
