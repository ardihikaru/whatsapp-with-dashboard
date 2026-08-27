package wa

// MediaKind selects which of the four media-message families a send or
// upload targets. This is this package's OWN type, not a re-export of
// waxum.MediaKind — no waxum type crosses the Backend seam (see backend.go's
// design note on GroupInfo for the same rule, and errors.go's mapErr for the
// error-vocabulary half of it). The four values and their string form
// mirror waxum's exactly (internal/waxum/models.go) because they end up on
// the wire verbatim (as the JSON body key SendUploaded nests the descriptor
// under, and as UploadMedia's media_type form field) — a mismatch here would
// silently talk to the wrong waxum endpoint.
type MediaKind string

const (
	MediaImage    MediaKind = "image"
	MediaVideo    MediaKind = "video"
	MediaAudio    MediaKind = "audio"
	MediaDocument MediaKind = "document"
)

// MediaDescriptor is this package's copy of waxum's "uploaded media" handle
// — the result of MediaStore.UploadMedia, the input to MediaStore.
// DownloadMedia, and (wrapped in OutboundMedia) the payload Backend.SendMedia
// delivers. Field-for-field mirror of waxum.MediaDescriptor for the same
// no-waxum-type-crosses-the-seam reason as MediaKind above.
type MediaDescriptor struct {
	URL           string
	DirectPath    string
	MediaKey      string
	FileSHA256    string
	FileEncSHA256 string
	FileLength    int64
	MediaType     string
	Mimetype      string
}

// OutboundMedia is what Backend.SendMedia sends: a previously uploaded
// descriptor plus the per-kind fields waxum's send endpoints accept.
//
// Caption/Filename/PTT are not all meaningful for every Kind — mirroring
// waxum.SendUploaded's own per-kind rules (see that method's doc comment,
// internal/waxum/media.go): image/video read Caption only; document
// REQUIRES Filename (sent even empty) and reads Caption; audio reads PTT
// only and never Caption. A waxumBackend implementation applies exactly
// that switch — this struct itself does not enforce it, the same way
// waxum.SendUploaded's parameters don't either.
type OutboundMedia struct {
	Kind       MediaKind
	Descriptor MediaDescriptor
	Caption    string
	Filename   string
	PTT        bool
}
