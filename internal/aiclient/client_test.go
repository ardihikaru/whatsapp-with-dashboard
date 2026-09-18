package aiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestSubmitJobSendsBasicAuthAndTheFixedWorkspaceID is RED without New's
// req.SetBasicAuth call and workspaceID's hardcoding: a client that forgot
// either would submit a job the server rejects (no credential) or one that
// runs against whatever workspace a caller passed in, defeating the
// plan's confirmed decision #5 that "ai-manager-chat" is never
// caller-configurable.
func TestSubmitJobSendsBasicAuthAndTheFixedWorkspaceID(t *testing.T) {
	var gotUser, gotPass string
	var gotBody JobRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var ok bool
		gotUser, gotPass, ok = r.BasicAuth()
		if !ok {
			t.Error("request carried no HTTP Basic credential")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(Job{ID: "job1", Status: JobQueued})
	}))
	defer srv.Close()

	c := New(srv.URL, "login-id", "secret-key", 5*time.Second)
	jobID, err := c.SubmitJob(context.Background(), JobRequest{WorkspaceID: workspaceID, Prompt: "say hi"})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	if jobID != "job1" {
		t.Errorf("jobID = %q, want %q", jobID, "job1")
	}
	if gotUser != "login-id" || gotPass != "secret-key" {
		t.Errorf("BasicAuth = (%q, %q), want (\"login-id\", \"secret-key\")", gotUser, gotPass)
	}
	if gotBody.WorkspaceID != "ai-manager-chat" {
		t.Errorf("workspace_id = %q, want %q", gotBody.WorkspaceID, "ai-manager-chat")
	}
}

// TestDoRetriesA503ViaTheSharedBackoffSchedule is RED without do()'s
// retry loop: a client that gave up on the first 503 would surface
// ErrRateLimited/an APIError to the caller instead of the eventual 200.
func TestDoRetriesA503ViaTheSharedBackoffSchedule(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(Job{ID: "job1", Status: JobCompleted})
	}))
	defer srv.Close()

	var delays []time.Duration
	c := New(srv.URL, "l", "s", 5*time.Second, WithRetryPolicy(5, func(attempt int) time.Duration {
		delays = append(delays, time.Duration(attempt))
		return time.Millisecond
	}))

	job, err := c.GetJob(context.Background(), "job1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Status != JobCompleted {
		t.Errorf("status = %q, want %q", job.Status, JobCompleted)
	}
	if attempts != 3 {
		t.Errorf("server saw %d attempts, want 3 (two 503s then success)", attempts)
	}
	if len(delays) != 2 {
		t.Errorf("retryDelay called %d times, want 2", len(delays))
	}
}

// TestExtractTextStripsFramingAndTakesTheFirstNonEmptyLine table-tests
// extractText's pure-function framing-stripping — the exact scenarios
// GenerateMessage's own doc comment names as what makes it reliable.
func TestExtractTextStripsFramingAndTakesTheFirstNonEmptyLine(t *testing.T) {
	cases := []struct {
		name  string
		stdin string
		want  string
	}{
		{"plain", "Selamat pagi tim!", "Selamat pagi tim!"},
		{"leading/trailing whitespace", "  Selamat pagi tim!  \n", "Selamat pagi tim!"},
		{"code fence", "```\nSelamat pagi tim!\n```", "Selamat pagi tim!"},
		{"blockquote marker", "> Selamat pagi tim!", "Selamat pagi tim!"},
		{"wrapped in quotes", `"Selamat pagi tim!"`, "Selamat pagi tim!"},
		{"multi-line takes first non-empty", "\n\nSelamat pagi tim!\nSemoga harimu menyenangkan.", "Selamat pagi tim!"},
		{"dash bullet", "- Selamat pagi tim!", "Selamat pagi tim!"},
		{"empty input", "", ""},
		{"only whitespace", "   \n\n  ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractText(c.stdin)
			if got != c.want {
				t.Errorf("extractText(%q) = %q, want %q", c.stdin, got, c.want)
			}
		})
	}
}

// TestExtractTextCapsOutputLength asserts the maxOutputChars cap actually
// fires, rather than only being documented.
func TestExtractTextCapsOutputLength(t *testing.T) {
	long := make([]byte, maxOutputChars+100)
	for i := range long {
		long[i] = 'a'
	}
	got := extractText(string(long))
	if len([]rune(got)) != maxOutputChars {
		t.Errorf("extractText returned %d runes, want the capped %d", len([]rune(got)), maxOutputChars)
	}
}

// TestGenerateMessageReturnsAnErrorNotAHangWhenTheContextDeadlineExpires is
// the explicit "bounded-deadline timeout -> error" test the plan names:
// GenerateMessage must never hang past its caller's own context deadline,
// even while a job is still legitimately "running" server-side.
func TestGenerateMessageReturnsAnErrorNotAHangWhenTheContextDeadlineExpires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(Job{ID: "job1", Status: JobQueued})
		case r.Method == http.MethodGet:
			// Always "running" — this job never finishes within the test's
			// short deadline.
			json.NewEncoder(w).Encode(Job{ID: "job1", Status: JobRunning})
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "l", "s", 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := GenerateMessage(ctx, c, GenerateOptions{Prompt: "say hi", PollInterval: 5 * time.Millisecond})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("GenerateMessage returned no error against a job that never completes, want a deadline error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("GenerateMessage took %s, want it to return promptly once the context deadline expires (not hang)", elapsed)
	}
}

// TestGenerateMessageRejectsAnEmptyPrompt is a cheap guard test: no request
// should ever reach the server for a caller mistake this obvious.
func TestGenerateMessageRejectsAnEmptyPrompt(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "l", "s", 5*time.Second)
	_, err := GenerateMessage(context.Background(), c, GenerateOptions{Prompt: "   "})
	if err != ErrEmptyPrompt {
		t.Fatalf("GenerateMessage with a blank prompt returned %v, want ErrEmptyPrompt", err)
	}
	if called {
		t.Error("GenerateMessage with a blank prompt reached the server, want it to refuse before submitting")
	}
}

// TestGenerateMessageSucceedsAndReturnsExtractedText is the positive path:
// submit, poll to completion, fetch logs, extract text.
func TestGenerateMessageSucceedsAndReturnsExtractedText(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/jobs":
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(Job{ID: "job1", Status: JobQueued})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/job1":
			polls++
			status := JobRunning
			if polls >= 2 {
				status = JobCompleted
			}
			json.NewEncoder(w).Encode(Job{ID: "job1", Status: status})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/job1/logs":
			json.NewEncoder(w).Encode(JobLogs{Stdout: "Selamat pagi tim!\n"})
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "l", "s", 5*time.Second)
	got, err := GenerateMessage(context.Background(), c, GenerateOptions{
		Prompt: "say good morning", PollInterval: 2 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("GenerateMessage: %v", err)
	}
	if got != "Selamat pagi tim!" {
		t.Errorf("GenerateMessage = %q, want %q", got, "Selamat pagi tim!")
	}
}
