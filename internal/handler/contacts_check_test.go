package handler

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/db"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/message"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/wasession"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/wa"
)

// newContactsCheckTestHandlers wires a real SQLite-backed wa.Manager around
// backend, following newPairingTestHandlers' (pairing_test.go) house
// pattern minus the renderer — checkContacts is driven directly, never
// through HTTP, so no page-rendering plumbing is needed here.
func newContactsCheckTestHandlers(t *testing.T, backend wa.Backend) (*handlers, *wa.ManagedSession) {
	t.Helper()
	pool, err := db.Open(filepath.Join(t.TempDir(), "test.db"), db.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := db.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	mgr := wa.NewManager(context.Background(), backend, wasession.NewRepository(pool),
		message.NewRepository(pool), wa.Config{SendRate: 1, SendBurst: 3, ReconcilePar: 2})
	s, err := mgr.Create(context.Background(), "gw", "+62 812-0000-9999")
	if err != nil {
		t.Fatal(err)
	}
	return &handlers{Deps: Deps{Manager: mgr}}, s
}

// TestCheckContactsReportsAnUnparseableNumberAsInvalidWithoutFailingItsSiblings
// is the single easiest thing to get wrong here (per the plan brief): one
// bad row must not sink the whole batch, and its valid siblings must still
// get real verdicts.
func TestCheckContactsReportsAnUnparseableNumberAsInvalidWithoutFailingItsSiblings(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			return []wa.ContactCheck{{Phone: "6281200000000", IsRegistered: true}}, nil
		},
	}
	h, s := newContactsCheckTestHandlers(t, backend)

	out, err := h.checkContacts(context.Background(), s.ID, []string{"not-a-number", "+62 812-0000-0000"})
	if err != nil {
		t.Fatalf("checkContacts returned an error for a per-row problem: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d verdicts, want 2: %+v", len(out), out)
	}
	if out[0].Status != contactStatusInvalid {
		t.Errorf("row 0 status = %q, want %q", out[0].Status, contactStatusInvalid)
	}
	if out[0].Error == "" {
		t.Error("row 0 Error is empty, want the NormalizePhone rejection reason")
	}
	if out[1].Status != contactStatusRegistered {
		t.Errorf("row 1 (valid sibling) status = %q, want %q — it must still get a real verdict", out[1].Status, contactStatusRegistered)
	}
}

// TestCheckContactsCollapsesDuplicatesInTheBackendQueryButKeepsOneRowPerInput
// asserts on what the fake actually received: the backend call must carry
// each distinct number exactly once, while the output still has one row per
// input line, in input order.
func TestCheckContactsCollapsesDuplicatesInTheBackendQueryButKeepsOneRowPerInput(t *testing.T) {
	var received []string
	backend := &fakeGatewayBackend{
		name: "fake",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			received = append([]string(nil), phones...)
			return []wa.ContactCheck{{Phone: "6281200000000", IsRegistered: true}}, nil
		},
	}
	h, s := newContactsCheckTestHandlers(t, backend)

	inputs := []string{"+62 812-0000-0000", "6281200000000", "+62 812-0000-0000"}
	out, err := h.checkContacts(context.Background(), s.ID, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d verdicts, want 3 (one per input line): %+v", len(out), out)
	}
	if !reflect.DeepEqual(received, []string{"6281200000000"}) {
		t.Fatalf("backend received %v, want exactly one distinct number", received)
	}
	for i, v := range out {
		if v.Status != contactStatusRegistered {
			t.Errorf("row %d status = %q, want %q", i, v.Status, contactStatusRegistered)
		}
	}
}

// TestCheckContactsReportsUnknownNotNotRegisteredWhenTheBackendIsSilentAboutANumber
// is the hazard the plan brief calls out by name: a valid number the
// backend simply never answered about must be "unknown", never
// "not_registered" — claiming "not on WhatsApp" for a number nobody
// actually checked is the confident guess CLAUDE.md's rule 2 forbids. Two
// numbers are queried so the silent one is unambiguous — with only one
// candidate queued, an orphaned result could plausibly be a canonicalised
// answer about that same number (see the canonicalisation test below);
// with two, a result matching neither leaves the other legitimately
// unaccounted for.
func TestCheckContactsReportsUnknownNotNotRegisteredWhenTheBackendIsSilentAboutANumber(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			// Two numbers queried; the backend answers about only one of
			// them, echoing its digits back exactly — the other gets no
			// verdict at all.
			return []wa.ContactCheck{{Phone: "6281200000000", IsRegistered: true}}, nil
		},
	}
	h, s := newContactsCheckTestHandlers(t, backend)

	out, err := h.checkContacts(context.Background(), s.ID, []string{"+62 812-0000-0000", "+62 899-0000-0000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d verdicts, want 2: %+v", len(out), out)
	}
	if out[0].Status != contactStatusRegistered {
		t.Errorf("row 0 status = %q, want %q", out[0].Status, contactStatusRegistered)
	}
	if out[1].Status != contactStatusUnknown {
		t.Errorf("row 1 status = %q, want %q (must NOT be %q)", out[1].Status, contactStatusUnknown, contactStatusNotRegistered)
	}
}

// TestCheckContactsMatchesAVerdictBackByNormalisedDigitsEvenWhenWaxumCanonicalisesThePhone
// proves matching goes through the RETURNED phone's digits, not the digits
// sent to the backend — waxum's usync can canonicalise the number it was
// asked about (wa.ContactCheck's doc comment).
func TestCheckContactsMatchesAVerdictBackByNormalisedDigitsEvenWhenWaxumCanonicalisesThePhone(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			// Sent "5511987654321"; waxum canonicalises away the leading 9.
			return []wa.ContactCheck{{Phone: "551187654321", JID: "551187654321@s.whatsapp.net", IsRegistered: true}}, nil
		},
	}
	h, s := newContactsCheckTestHandlers(t, backend)

	out, err := h.checkContacts(context.Background(), s.ID, []string{"+55 11 98765-4321"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d verdicts, want 1: %+v", len(out), out)
	}
	if out[0].Status != contactStatusRegistered {
		t.Fatalf("status = %q, want %q — a canonicalised phone must still match back to its input", out[0].Status, contactStatusRegistered)
	}
	if out[0].Phone != "551187654321" {
		t.Errorf("Phone = %q, want waxum's canonicalised %q surfaced, not the digits sent", out[0].Phone, "551187654321")
	}
}

// TestCheckContactsDropsBlankLinesInsteadOfReportingThemInvalid proves a
// trailing textarea newline (or any whitespace-only line) is silently
// dropped before normalisation runs, rather than becoming a spurious
// "invalid" row.
func TestCheckContactsDropsBlankLinesInsteadOfReportingThemInvalid(t *testing.T) {
	backend := &fakeGatewayBackend{
		name: "fake",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			return []wa.ContactCheck{{Phone: "6281200000000", IsRegistered: true}}, nil
		},
	}
	h, s := newContactsCheckTestHandlers(t, backend)

	out, err := h.checkContacts(context.Background(), s.ID, []string{"+62 812-0000-0000", "", "   ", "\t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d verdicts, want 1 (blank lines dropped): %+v", len(out), out)
	}
	if out[0].Status != contactStatusRegistered {
		t.Errorf("status = %q, want %q", out[0].Status, contactStatusRegistered)
	}
}

// TestCheckContactsRejectsMoreThanTheCapWithoutCallingTheBackend proves the
// 100-number cap is enforced BEFORE any backend call — counted after blank
// lines are dropped.
func TestCheckContactsRejectsMoreThanTheCapWithoutCallingTheBackend(t *testing.T) {
	called := false
	backend := &fakeGatewayBackend{
		name: "fake",
		checkContactsFn: func(ctx context.Context, sessionID string, phones []string) ([]wa.ContactCheck, error) {
			called = true
			return nil, nil
		},
	}
	h, s := newContactsCheckTestHandlers(t, backend)

	inputs := make([]string, maxVerifyPhones+1)
	for i := range inputs {
		inputs[i] = "+62 812-0000-0000"
	}
	_, err := h.checkContacts(context.Background(), s.ID, inputs)
	if err == nil {
		t.Fatal("checkContacts did not error over the cap")
	}
	if called {
		t.Error("backend was called despite the cap being exceeded")
	}
}

// TestCheckContactsReturnsUnsupportedWhenTheBackendHasNoContactDirectory
// proves the function surfaces wa.ErrUnsupported as a genuine function
// error (not a per-row status) when the active backend implements no
// ContactDirectory at all.
func TestCheckContactsReturnsUnsupportedWhenTheBackendHasNoContactDirectory(t *testing.T) {
	h, s := newContactsCheckTestHandlers(t, whatsmeowShapedBackend())

	_, err := h.checkContacts(context.Background(), s.ID, []string{"+62 812-0000-0000"})
	if err == nil {
		t.Fatal("checkContacts did not error against a backend with no ContactDirectory")
	}
}
