package handler

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/auth"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/apiclient"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/call"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/renderer"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/storage"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// fakeGatewayBackend is a single wa.Backend fake covering every method
// Phase 6a's handlers reach: it always structurally satisfies wa.MediaStore
// (UploadMedia/DownloadMedia below), and its call methods delegate to
// configurable hooks — nil means "not supported," the same default every
// other unused method on this fake answers with, mirroring
// fakeStartPairingBackend/fakeBackend's house pattern (pairing_test.go,
// internal/wa/backend_test.go) of panicking on anything genuinely
// unexpected and quietly ErrUnsupported-ing everything this phase's tests
// don't specifically drive.
type fakeGatewayBackend struct {
	name string

	uploadFn func(ctx context.Context, sessionID string, kind wa.MediaKind, filename, mimetype string, data []byte) (wa.MediaDescriptor, error)
	ttsFn    func(to wa.JID, text, voice string) (wa.CallOutcome, error)
	playFn   func(to wa.JID, audioURL string) (wa.CallOutcome, error)
	termFn   func(peer wa.JID, callID, reason string) error
}

func (f *fakeGatewayBackend) Name() string { return f.name }
func (f *fakeGatewayBackend) StartPairing(context.Context, *wa.ManagedSession, wa.PairMode) error {
	return wa.ErrUnsupported
}
func (f *fakeGatewayBackend) Connect(context.Context, *wa.ManagedSession) error {
	return wa.ErrUnsupported
}
func (f *fakeGatewayBackend) Disconnect(context.Context, *wa.ManagedSession) error { return nil }
func (f *fakeGatewayBackend) Logout(context.Context, *wa.ManagedSession) error     { return nil }
func (f *fakeGatewayBackend) SendText(context.Context, *wa.ManagedSession, wa.JID, string) (string, error) {
	return "", wa.ErrUnsupported
}
func (f *fakeGatewayBackend) JoinedGroups(context.Context, *wa.ManagedSession) ([]wa.GroupInfo, error) {
	return nil, wa.ErrUnsupported
}
func (f *fakeGatewayBackend) SendMedia(context.Context, *wa.ManagedSession, wa.JID, wa.OutboundMedia) (string, error) {
	return "", wa.ErrUnsupported
}
func (f *fakeGatewayBackend) CallTTS(_ context.Context, _ *wa.ManagedSession, to wa.JID, text, voice string) (wa.CallOutcome, error) {
	if f.ttsFn != nil {
		return f.ttsFn(to, text, voice)
	}
	return wa.CallOutcome{}, wa.ErrUnsupported
}
func (f *fakeGatewayBackend) CallPlay(_ context.Context, _ *wa.ManagedSession, to wa.JID, audioURL string) (wa.CallOutcome, error) {
	if f.playFn != nil {
		return f.playFn(to, audioURL)
	}
	return wa.CallOutcome{}, wa.ErrUnsupported
}
func (f *fakeGatewayBackend) CallTerminate(_ context.Context, _ *wa.ManagedSession, peer wa.JID, callID, reason string) error {
	if f.termFn != nil {
		return f.termFn(peer, callID, reason)
	}
	return wa.ErrUnsupported
}
func (f *fakeGatewayBackend) Live(*wa.ManagedSession) bool { return false }
func (f *fakeGatewayBackend) Close()                       {}

// UploadMedia/DownloadMedia give fakeGatewayBackend wa.MediaStore's method
// set unconditionally; uploadFn == nil answers ErrUnsupported, which is
// exactly how a whatsmeow-backed deployment behaves (Manager.MediaStore's
// type assertion would fail there instead, but the OBSERVABLE HTTP result —
// 501 via writeWAError — is identical, which is what these tests assert).
func (f *fakeGatewayBackend) UploadMedia(ctx context.Context, sessionID string, kind wa.MediaKind, filename, mimetype string, data []byte) (wa.MediaDescriptor, error) {
	if f.uploadFn != nil {
		return f.uploadFn(ctx, sessionID, kind, filename, mimetype, data)
	}
	return wa.MediaDescriptor{}, wa.ErrUnsupported
}
func (f *fakeGatewayBackend) DownloadMedia(context.Context, string, wa.MediaDescriptor, wa.MediaKind) ([]byte, error) {
	return nil, wa.ErrUnsupported
}

var _ wa.MediaStore = (*fakeGatewayBackend)(nil)

// callLog is a small mutex-protected call-order recorder, shared between a
// fakeObjectStore and a fakeGatewayBackend so
// TestMediaSendArchivesToObjectStorageBeforeUploadingToTheBackend can
// assert ordering across two otherwise-independent fakes.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (c *callLog) record(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, s)
}

func (c *callLog) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.calls))
	copy(out, c.calls)
	return out
}

// fakeObjectStore implements storage.ObjectStore without live MinIO —
// house pattern per the phase brief ("Deps.Objects is an interface — use a
// fake, not live MinIO").
type fakeObjectStore struct {
	log    *callLog
	putErr error
}

func (f *fakeObjectStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if f.log != nil {
		f.log.record("put:" + key)
	}
	io.Copy(io.Discard, r)
	return f.putErr
}
func (f *fakeObjectStore) PresignedGet(ctx context.Context, key string) (string, error) {
	return "https://example.invalid/" + key, nil
}
func (f *fakeObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, wa.ErrUnsupported
}

var _ storage.ObjectStore = (*fakeObjectStore)(nil)

// mediaTestConfig configures newMediaTestServer; zero-valued fields resolve
// to a sensible default (see the function body).
type mediaTestConfig struct {
	backend       wa.Backend
	objects       storage.ObjectStore
	callsEnabled  bool
	mediaMaxBytes int64
	// mediaMaxBytes{Image,Video,Audio,Document}, when non-zero, feed
	// Deps.MediaMaxBytes{Image,Video,Audio,Document} directly — otherwise
	// left at zero, which handlers.mediaKindLimit (api_media.go) falls
	// back to mediaMaxBytes for, exactly as an untouched Deps literal does.
	mediaMaxBytesImage    int64
	mediaMaxBytesVideo    int64
	mediaMaxBytesAudio    int64
	mediaMaxBytesDocument int64
}

// newMediaTestServer wires a full handler.New(Deps{...}) server around a
// SQLite-backed wa.Manager, following newPairingTestHandlers/
// newAuthTestServer's house pattern but with the Phase 6a deps filled in.
// Returns the server, the one created (never activated — see this file's
// header comment on why that's fine for these tests) session, the api
// client repo (for apiToken) and the message repo (to assert on the audit
// row a media send leaves behind).
func newMediaTestServer(t *testing.T, cfg mediaTestConfig) (http.Handler, *wa.ManagedSession, *apiclient.Repository, *message.SQLRepository, call.Repository) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	webFS, err := fs.Sub(wsm.FS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rend, err := renderer.New(webFS, "")
	if err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewStore(time.Hour)
	t.Cleanup(sessions.Close)
	apiClients := apiclient.NewRepository(pool)
	msgRepo := message.NewRepository(pool)
	callRepo := call.NewRepository(pool)

	mgr := wa.NewManager(context.Background(), cfg.backend, wasession.NewRepository(pool),
		msgRepo, wa.Config{SendRate: 100, SendBurst: 5, ReconcilePar: 2})
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-9999")
	if err != nil {
		t.Fatal(err)
	}

	maxBytes := cfg.mediaMaxBytes
	if maxBytes == 0 {
		maxBytes = 4 << 20
	}

	srv := New(Deps{
		Renderer:               rend,
		Sessions:               sessions,
		Middleware:             auth.NewMiddleware(sessions, apiClients, ""),
		Manager:                mgr,
		Messages:               msgRepo,
		MessageStats:           msgRepo,
		APIClients:             apiClients,
		Calls:                  callRepo,
		Objects:                cfg.objects,
		MediaMaxBytes:          maxBytes,
		MediaMaxBytesImage:     cfg.mediaMaxBytesImage,
		MediaMaxBytesVideo:     cfg.mediaMaxBytesVideo,
		MediaMaxBytesAudio:     cfg.mediaMaxBytesAudio,
		MediaMaxBytesDocument:  cfg.mediaMaxBytesDocument,
		MediaUploadTimeout:     5 * time.Second,
		MediaUploadConcurrency: 2,
		MediaArchive:           cfg.objects != nil,
		CallsEnabled:           cfg.callsEnabled,
		SessionTTL:             time.Hour,
		AssetsFS:               wsm.FS,
	})
	return srv, s, apiClients, msgRepo, callRepo
}
