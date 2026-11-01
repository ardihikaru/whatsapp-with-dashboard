package statussched

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/contact"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

type fakePoster struct {
	text, image, video int
	lastBG             uint32
	lastCaption        string
	lastDesc           wa.MediaDescriptor
	err                error
}

func (f *fakePoster) PostStatusText(_ context.Context, _ string, _ string, bg uint32, _ []string) (string, error) {
	f.text++
	f.lastBG = bg
	return "wa-text", f.err
}
func (f *fakePoster) PostStatusImage(_ context.Context, _ string, d wa.MediaDescriptor, c string, _ []string) (string, error) {
	f.image++
	f.lastDesc, f.lastCaption = d, c
	return "wa-image", f.err
}
func (f *fakePoster) PostStatusVideo(_ context.Context, _ string, d wa.MediaDescriptor, c string, _ []string) (string, error) {
	f.video++
	f.lastDesc, f.lastCaption = d, c
	return "wa-video", f.err
}

type fakeStore struct {
	uploads  int
	gotKind  wa.MediaKind
	gotName  string
	gotMime  string
	gotBytes []byte
}

func (f *fakeStore) UploadMedia(_ context.Context, _ string, k wa.MediaKind, name, mime string, data []byte) (wa.MediaDescriptor, error) {
	f.uploads++
	f.gotKind, f.gotName, f.gotMime, f.gotBytes = k, name, mime, data
	return wa.MediaDescriptor{Mimetype: mime, URL: "fresh"}, nil
}
func (f *fakeStore) DownloadMedia(context.Context, string, wa.MediaDescriptor, wa.MediaKind) ([]byte, error) {
	return nil, nil
}

type fakeObjects struct {
	data map[string][]byte
	gets []string
}

func (f *fakeObjects) Put(context.Context, string, io.Reader, int64, string) error { return nil }
func (f *fakeObjects) PresignedGet(context.Context, string) (string, error)        { return "", nil }
func (f *fakeObjects) Delete(context.Context, string) error                        { return nil }
func (f *fakeObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.gets = append(f.gets, key)
	d, ok := f.data[key]
	if !ok {
		return nil, errors.New("no such object")
	}
	return io.NopCloser(bytes.NewReader(d)), nil
}

type logSink struct {
	rows []*message.Message
	errs []error
}

func (l *logSink) log(_ context.Context, m *message.Message, _ string, postErr error) error {
	l.rows = append(l.rows, m)
	l.errs = append(l.errs, postErr)
	return nil
}

func TestATextPostGoesThroughPostStatusTextAndLeavesOneAuditRow(t *testing.T) {
	sp, sink := &fakePoster{}, &logSink{}
	sc := sched.Schedule{ID: "a", SessionID: "s1", Kind: sched.KindText, Text: "hi", BgARGB: 42}
	id, err := postVia(context.Background(), Post{Schedule: sc, Recipients: []string{"1@s.whatsapp.net"}}, sp, nil, nil, sink.log)
	if err != nil || id != "wa-text" || sp.text != 1 || sp.lastBG != 42 {
		t.Fatalf("id=%q err=%v poster=%+v", id, err, sp)
	}
	if len(sink.rows) != 1 || sink.rows[0].ToPhone != "status@broadcast" || sink.rows[0].Body != "hi" {
		t.Errorf("audit rows = %+v", sink.rows)
	}
}

func TestAnImagePostRefetchesTheStoredFileAndUploadsItFreshEveryTime(t *testing.T) {
	sp, st, sink := &fakePoster{}, &fakeStore{}, &logSink{}
	obj := &fakeObjects{data: map[string][]byte{"k1": []byte("PNGDATA")}}
	sc := sched.Schedule{ID: "a", SessionID: "s1", Kind: sched.KindImage, Caption: "cap",
		MediaObjectKey: "k1", MediaFilename: "x.png", MediaContentType: "image/png"}
	for i := 0; i < 2; i++ {
		if _, err := postVia(context.Background(), Post{Schedule: sc, Recipients: []string{"1@s.whatsapp.net"}}, sp, st, obj, sink.log); err != nil {
			t.Fatal(err)
		}
	}
	if st.uploads != 2 || len(obj.gets) != 2 {
		t.Errorf("uploads=%d gets=%d, want a fresh fetch+upload per fire (descriptors are never persisted)", st.uploads, len(obj.gets))
	}
	if st.gotKind != wa.MediaImage || string(st.gotBytes) != "PNGDATA" || st.gotName != "x.png" || st.gotMime != "image/png" {
		t.Errorf("upload args wrong: %+v", st)
	}
	if sp.image != 2 || sp.lastCaption != "cap" || sp.lastDesc.URL != "fresh" {
		t.Errorf("poster = %+v", sp)
	}
	if sink.rows[0].MediaKind != "image" || sink.rows[0].Body != "cap" {
		t.Errorf("audit row = %+v", sink.rows[0])
	}
}

func TestAVideoPostUsesPostStatusVideo(t *testing.T) {
	sp, st := &fakePoster{}, &fakeStore{}
	obj := &fakeObjects{data: map[string][]byte{"v": []byte("MP4")}}
	sc := sched.Schedule{ID: "a", SessionID: "s1", Kind: sched.KindVideo, MediaObjectKey: "v", MediaFilename: "v.mp4", MediaContentType: "video/mp4"}
	if _, err := postVia(context.Background(), Post{Schedule: sc}, sp, st, obj, (&logSink{}).log); err != nil {
		t.Fatal(err)
	}
	if sp.video != 1 || sp.image != 0 || st.gotKind != wa.MediaVideo {
		t.Errorf("video=%d image=%d kind=%s", sp.video, sp.image, st.gotKind)
	}
}

func TestMediaPostsFailClearlyWithoutObjectStorageAndStillAuditTheFailure(t *testing.T) {
	sp, sink := &fakePoster{}, &logSink{}
	sc := sched.Schedule{ID: "a", SessionID: "s1", Kind: sched.KindImage, MediaObjectKey: "k"}
	_, err := postVia(context.Background(), Post{Schedule: sc}, sp, &fakeStore{}, nil, sink.log)
	if !errors.Is(err, ErrNoObjectStore) {
		t.Fatalf("err = %v, want ErrNoObjectStore", err)
	}
	if sp.image != 0 || len(sink.rows) != 1 || sink.errs[0] == nil {
		t.Errorf("posted anyway or missing failed audit row: image=%d rows=%d", sp.image, len(sink.rows))
	}
}

func TestAPostFailureIsAuditedAndReturned(t *testing.T) {
	sp, sink := &fakePoster{err: errors.New("boom")}, &logSink{}
	sc := sched.Schedule{ID: "a", SessionID: "s1", Kind: sched.KindText, Text: "x"}
	if _, err := postVia(context.Background(), Post{Schedule: sc}, sp, nil, nil, sink.log); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
	if len(sink.rows) != 1 || sink.errs[0] == nil {
		t.Errorf("failure not audited: %+v", sink)
	}
}

func TestContactJIDPrefersTheVerifiedJIDElseDerivesOneFromThePhone(t *testing.T) {
	if got := ContactJID(contact.Contact{Phone: "6281", JID: "6281@s.whatsapp.net"}); got != "6281@s.whatsapp.net" {
		t.Errorf("got %q", got)
	}
	if got := ContactJID(contact.Contact{Phone: "6281234"}); got != wa.ToJID("6281234").String() || got == "" {
		t.Errorf("derived jid = %q", got)
	}
}
