package waxum

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestOperationsHitTheirDocumentedMethodsAndPaths is the single highest-value
// test in this file: a wrong method or path here ships an endpoint that
// compiles, is called, and hits nothing real — the fake server would 404 (or
// worse, hit a DIFFERENT real waxum route) in production while every test
// that only checks "no error" stays green.
func TestOperationsHitTheirDocumentedMethodsAndPaths(t *testing.T) {
	var gotMethod, gotPath string
	var respBody []byte
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if respBody != nil {
			w.Write(respBody)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name       string
		wantMethod string
		wantPath   string
		body       []byte
		call       func() error
	}{
		{"Pause", http.MethodPost, "/api/v1/sessions/sess-1/pause", []byte(`{"paused":true}`), func() error {
			_, err := client.Pause(context.Background(), "sess-1")
			return err
		}},
		{"Resume", http.MethodPost, "/api/v1/sessions/sess-1/resume", []byte(`{"paused":false}`), func() error {
			_, err := client.Resume(context.Background(), "sess-1")
			return err
		}},
		{"AutoReconnect", http.MethodGet, "/api/v1/sessions/sess-1/reconnect", []byte(`{"enabled":true,"error_count":0}`), func() error {
			_, err := client.AutoReconnect(context.Background(), "sess-1")
			return err
		}},
		{"SetAutoReconnect", http.MethodPut, "/api/v1/sessions/sess-1/reconnect", []byte(`{"enabled":true,"error_count":0}`), func() error {
			_, err := client.SetAutoReconnect(context.Background(), "sess-1", true)
			return err
		}},
		{"HistorySync", http.MethodGet, "/api/v1/sessions/sess-1/history-sync", []byte(`{"skip_history_sync":true}`), func() error {
			_, err := client.HistorySync(context.Background(), "sess-1")
			return err
		}},
		{"SetHistorySync", http.MethodPut, "/api/v1/sessions/sess-1/history-sync", []byte(`{"skip_history_sync":true}`), func() error {
			_, err := client.SetHistorySync(context.Background(), "sess-1", true)
			return err
		}},
		{"AppStateResync", http.MethodPost, "/api/v1/sessions/sess-1/appstate/resync",
			[]byte(`{"synced":[],"fatal":[],"retryable":[],"skipped":[],"all_synced":true}`), func() error {
				_, err := client.AppStateResync(context.Background(), "sess-1", []string{"critical_block"}, "full")
				return err
			}},
		{"ReportSpam", http.MethodPost, "/api/v1/sessions/sess-1/spam/report",
			[]byte(`{"success":true,"report_id":"r1"}`), func() error {
				_, err := client.ReportSpam(context.Background(), "sess-1",
					SpamReport{MessageID: "m1", MessageTimestamp: 1700000000})
				return err
			}},
		{"TCTokenIssue", http.MethodPost, "/api/v1/sessions/sess-1/tctoken/issue", []byte(`{"tokens":[]}`), func() error {
			_, err := client.TCTokenIssue(context.Background(), "sess-1", []string{"628123@s.whatsapp.net"})
			return err
		}},
		{"TCTokenGet", http.MethodGet, "/api/v1/sessions/sess-1/tctoken/628123@s.whatsapp.net",
			[]byte(`{"jid":"628123@s.whatsapp.net","token_timestamp":0,"sender_timestamp":0,"found":true}`), func() error {
				_, err := client.TCTokenGet(context.Background(), "sess-1", "628123@s.whatsapp.net")
				return err
			}},
		{"TCTokenList", http.MethodGet, "/api/v1/sessions/sess-1/tctoken/list", []byte(`{"jids":[]}`), func() error {
			_, err := client.TCTokenList(context.Background(), "sess-1")
			return err
		}},
		{"TCTokenPrune", http.MethodDelete, "/api/v1/sessions/sess-1/tctoken/expired", []byte(`{"pruned_count":0}`), func() error {
			_, err := client.TCTokenPrune(context.Background(), "sess-1")
			return err
		}},
		{"CheckContacts", http.MethodPost, "/api/v1/sessions/sess-1/contacts/check", []byte(`{"results":[]}`), func() error {
			_, err := client.CheckContacts(context.Background(), "sess-1", []string{"559999999999"})
			return err
		}},
	}

	if len(cases) != 13 {
		t.Fatalf("table has %d cases, want 13 (one per documented operation, plus CheckContacts from contacts.go)", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			respBody = tc.body
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if gotMethod != tc.wantMethod {
				t.Errorf("%s: method = %q, want %q", tc.name, gotMethod, tc.wantMethod)
			}
			if gotPath != tc.wantPath {
				t.Errorf("%s: path = %q, want %q", tc.name, gotPath, tc.wantPath)
			}
		})
	}
}

// TestSpamReportSendsTheRequiredMessageTimestamp guards the bug fixed in
// this phase: waxum's SpamReportRequest.message_timestamp has no serde
// default (src/models/messages.rs), so a Go caller that never puts it on
// the wire gets a 422 on every single call. This asserts the field is
// present and numeric, not merely that ReportSpam does not error against a
// permissive fake (which would stay green even with the field missing
// entirely).
func TestSpamReportSendsTheRequiredMessageTimestamp(t *testing.T) {
	var gotBody map[string]any
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(SpamReportResponse{Success: true})
	})

	if _, err := client.ReportSpam(context.Background(), "sess-1", SpamReport{
		MessageID:        "m1",
		MessageTimestamp: 1700000000,
	}); err != nil {
		t.Fatalf("ReportSpam: %v", err)
	}

	ts, ok := gotBody["message_timestamp"]
	if !ok {
		t.Fatal("request body has no \"message_timestamp\" field — every call would 422 against real waxum")
	}
	if got, want := ts.(float64), float64(1700000000); got != want {
		t.Errorf("message_timestamp = %v, want %v", got, want)
	}
	if _, ok := gotBody["from_jid"]; ok {
		t.Errorf("from_jid present in body though SpamReport.FromJID was empty; want omitted")
	}
}

// TestReportSpamDecodesTheReportIDResponse guards ReportSpam actually
// returning waxum's decoded {success, report_id} instead of discarding it
// (an earlier version of this method passed `nil` as the decode target).
func TestReportSpamDecodesTheReportIDResponse(t *testing.T) {
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "report_id": "rep-123"})
	})

	got, err := client.ReportSpam(context.Background(), "sess-1", SpamReport{
		MessageID: "m1", MessageTimestamp: 1700000000,
	})
	if err != nil {
		t.Fatalf("ReportSpam: %v", err)
	}
	if !got.Success {
		t.Errorf("Success = false, want true")
	}
	if got.ReportID == nil || *got.ReportID != "rep-123" {
		t.Errorf("ReportID = %v, want \"rep-123\"", got.ReportID)
	}
}

// TestTCTokenPruneUsesDeleteOnTheExpiredPath is a dedicated, narrower guard
// for the one trap most likely to regress silently: TCTokenIssue is a POST,
// TCTokenPrune is a DELETE, and both mutate server state, so swapping them
// would compile and even "succeed" against a permissive fake but 404/405
// against the real waxum route.
func TestTCTokenPruneUsesDeleteOnTheExpiredPath(t *testing.T) {
	var gotMethod, gotPath string
	client, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Write([]byte(`{"pruned_count":3}`))
	})

	n, err := client.TCTokenPrune(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("TCTokenPrune: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE (not POST, unlike TCTokenIssue)", gotMethod)
	}
	wantPath := "/api/v1/sessions/sess-1/tctoken/expired"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if n != 3 {
		t.Errorf("pruned = %d, want 3", n)
	}
}
