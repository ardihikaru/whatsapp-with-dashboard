package message

import (
	"errors"
	"testing"
)

// TestGetByIDReturnsTheStoredRowIncludingItsErrorText pins the by-id read
// selfconvo's Recover reconciliation needs (internal/selfconvo/runner.go):
// a failed row's Error text must round-trip so Recover can hand it to
// convo.Repository.MarkTurnFailed verbatim rather than inventing a reason.
func TestGetByIDReturnsTheStoredRowIncludingItsErrorText(t *testing.T) {
	r, ctx := newTestRepo(t)
	// Error is not an Insert column (only MarkFailed writes it — see
	// Insert's own column list) — seed a queued row, then fail it the same
	// way the send worker does.
	id := r.seed(t, ctx, Message{Status: StatusQueued, Body: "halo"})
	if err := r.MarkFailed(ctx, id, "waxum: boom", 3); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	got, err := r.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, StatusFailed)
	}
	if got.Error != "waxum: boom" {
		t.Errorf("Error = %q, want %q", got.Error, "waxum: boom")
	}
	if got.Body != "halo" {
		t.Errorf("Body = %q, want %q", got.Body, "halo")
	}
}

// TestGetByIDReturnsErrNotFoundForAnUnknownID pins the "genuinely unknown"
// signal Recover relies on to fall back to "interrupted" rather than
// guessing.
func TestGetByIDReturnsErrNotFoundForAnUnknownID(t *testing.T) {
	r, ctx := newTestRepo(t)
	if _, err := r.GetByID(ctx, "does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByID on an unknown id = %v, want ErrNotFound", err)
	}
}

// TestInsertPersistsTheMediaColumns proves the five 0004 columns round-trip
// through Insert and Recent for a real media send.
func TestInsertPersistsTheMediaColumns(t *testing.T) {
	r, ctx := newTestRepo(t)
	r.seed(t, ctx, Message{
		Status:         StatusSent,
		MediaKind:      MediaKindImage,
		MediaFilename:  "cat.jpg",
		MediaMime:      "image/jpeg",
		MediaSize:      12345,
		MediaObjectKey: "wa/sess-1/cat.jpg",
	})

	got, err := r.Recent(ctx, "sess-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Recent returned %d rows, want 1", len(got))
	}
	m := got[0]
	if m.MediaKind != MediaKindImage {
		t.Errorf("MediaKind = %q, want %q", m.MediaKind, MediaKindImage)
	}
	if m.MediaFilename != "cat.jpg" {
		t.Errorf("MediaFilename = %q, want %q", m.MediaFilename, "cat.jpg")
	}
	if m.MediaMime != "image/jpeg" {
		t.Errorf("MediaMime = %q, want %q", m.MediaMime, "image/jpeg")
	}
	if m.MediaSize != 12345 {
		t.Errorf("MediaSize = %d, want 12345", m.MediaSize)
	}
	if m.MediaObjectKey != "wa/sess-1/cat.jpg" {
		t.Errorf("MediaObjectKey = %q, want %q", m.MediaObjectKey, "wa/sess-1/cat.jpg")
	}
}

// MediaKindImage exists only for this test file's readability — the domain
// package deliberately does not re-export internal/wa.MediaKind (see
// internal/wa/media.go's doc comment on why no waxum/wa type crosses into
// this package); it is just the literal string internal/wa.MediaImage holds.
const MediaKindImage = "image"

// TestRecentReadsBackTheMediaColumns proves a mixed page (one media row, one
// plain text row) reads back correctly for each row independently — a media
// column on one row leaking onto another would only show up with more than
// one row in play.
func TestRecentReadsBackTheMediaColumns(t *testing.T) {
	r, ctx := newTestRepo(t)
	r.seed(t, ctx, Message{Status: StatusSent, Body: "hello"})
	r.seed(t, ctx, Message{
		Status:        StatusSent,
		Body:          "a caption",
		MediaKind:     "document",
		MediaFilename: "invoice.pdf",
		MediaMime:     "application/pdf",
		MediaSize:     999,
	})

	got, err := r.Recent(ctx, "sess-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent returned %d rows, want 2", len(got))
	}
	// Recent orders DESC by created_at; both rows share a timestamp resolution
	// of one second in this test, so key off Body instead of index.
	byBody := map[string]Message{}
	for _, m := range got {
		byBody[m.Body] = m
	}
	text := byBody["hello"]
	if text.MediaKind != "" || text.MediaFilename != "" || text.MediaMime != "" || text.MediaSize != 0 {
		t.Errorf("text row carries media data: %+v", text)
	}
	doc := byBody["a caption"]
	if doc.MediaKind != "document" || doc.MediaFilename != "invoice.pdf" ||
		doc.MediaMime != "application/pdf" || doc.MediaSize != 999 {
		t.Errorf("document row media data = %+v, want kind=document filename=invoice.pdf mime=application/pdf size=999", doc)
	}
}

// TestInsertLeavesTheMediaColumnsNullForAPlainTextMessage is the NULL-not-
// empty-string decision from migrations/0004's header: a text message's
// media columns must be genuinely NULL in the database, not "" / 0, so a
// COUNT/GROUP BY over media_kind treats every text row as one NULL bucket
// rather than a distinct empty-string bucket.
func TestInsertLeavesTheMediaColumnsNullForAPlainTextMessage(t *testing.T) {
	r, ctx := newTestRepo(t)
	r.seed(t, ctx, Message{Status: StatusSent, Body: "plain text, no media"})

	var mediaKind, mediaFilename, mediaMime, mediaObjectKey any
	var mediaSize any
	if err := r.pool.QueryRowContext(ctx,
		`SELECT media_kind, media_filename, media_mime, media_size, media_object_key
		 FROM wa_messages WHERE session_id = 'sess-1'`,
	).Scan(&mediaKind, &mediaFilename, &mediaMime, &mediaSize, &mediaObjectKey); err != nil {
		t.Fatal(err)
	}
	if mediaKind != nil {
		t.Errorf("media_kind = %v, want SQL NULL", mediaKind)
	}
	if mediaFilename != nil {
		t.Errorf("media_filename = %v, want SQL NULL", mediaFilename)
	}
	if mediaMime != nil {
		t.Errorf("media_mime = %v, want SQL NULL", mediaMime)
	}
	if mediaSize != nil {
		t.Errorf("media_size = %v, want SQL NULL, not the number 0", mediaSize)
	}
	if mediaObjectKey != nil {
		t.Errorf("media_object_key = %v, want SQL NULL", mediaObjectKey)
	}
}
