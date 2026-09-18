package aiclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// workspaceID is HARDCODED, never an operator-configurable field or taken
// from GenerateOptions — see the phase plan's confirmed decision #5:
// ai-codex-cli jobs for this feature always run against the generic scratch
// workspace "ai-manager-chat", never "self" (the ai-manager repo itself).
const workspaceID = "ai-manager-chat"

// defaultPollInterval is how often GenerateMessage polls GetJob while
// waiting for a submitted job to finish.
const defaultPollInterval = 2 * time.Second

// maxOutputChars caps the returned text's length — a runaway job output
// must never become an oversized WhatsApp message.
const maxOutputChars = 500

// GenerateOptions configures one GenerateMessage call.
type GenerateOptions struct {
	// Prompt is the FULL instruction sent as the job's prompt — the caller
	// (internal/groupbroadcast/wording.go) is responsible for composing it,
	// including telling codex explicitly to reply with ONLY the final
	// WhatsApp sentence, in the requested language/register, nothing else
	// (see this file's own doc comment on why that is what makes
	// extractText reliable).
	Prompt string
	// Model overrides the server-side default (empty means "let the server
	// choose", currently gpt-5.5).
	Model string
	// PollInterval overrides defaultPollInterval; tests set this low to
	// avoid real sleeps.
	PollInterval time.Duration
}

var (
	// ErrEmptyPrompt is returned when GenerateOptions.Prompt is blank —
	// there is nothing meaningful to submit.
	ErrEmptyPrompt = errors.New("aiclient: prompt is empty")
	// ErrJobFailed is returned when the submitted job reaches a terminal
	// non-completed status (failed/cancelled).
	ErrJobFailed = errors.New("aiclient: job did not complete")
	// ErrEmptyOutput is returned when a completed job's logs, after
	// extractText's framing-stripping, carry no usable text at all.
	ErrEmptyOutput = errors.New("aiclient: job produced no usable text")
)

// GenerateMessage submits opt.Prompt as a job against the hardcoded
// "ai-manager-chat" workspace, polls it to completion inside ctx's own
// deadline (the caller — internal/groupbroadcast's Planner — is expected to
// wrap ctx in a bounded context.WithTimeout, default 90s via
// WSM_GROUP_BROADCAST_AI_DEADLINE), and on success extracts the final
// one-line WhatsApp text from the job's stdout.
//
// GenerateMessage NEVER falls back itself — any failure (submit error, poll
// timeout, job failure, empty output) is returned as an error. The caller
// (internal/groupbroadcast/wording.go) owns the decision to fall back to
// the local paraphrase engine, keeping this function's own tests crisp:
// every case here is "did the AI call succeed", not "what should we say
// instead if it didn't".
//
// A ctx deadline expiring mid-poll returns ctx.Err() promptly — the job
// itself is left running server-side (not cancelled): ai-codex-cli is an
// async job runner, and cancelling on every caller-side timeout would
// needlessly waste work a slow-but-eventually-successful job might still
// produce, work this call simply won't wait around for.
func GenerateMessage(ctx context.Context, c *Client, opt GenerateOptions) (string, error) {
	if strings.TrimSpace(opt.Prompt) == "" {
		return "", ErrEmptyPrompt
	}
	poll := opt.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}

	jobID, err := c.SubmitJob(ctx, JobRequest{WorkspaceID: workspaceID, Prompt: opt.Prompt, Model: opt.Model})
	if err != nil {
		return "", fmt.Errorf("aiclient: submit job: %w", err)
	}

	for {
		job, err := c.GetJob(ctx, jobID)
		if err != nil {
			return "", fmt.Errorf("aiclient: get job %s: %w", jobID, err)
		}
		if IsJobTerminal(job.Status) {
			if job.Status != JobCompleted {
				reason := job.Error
				if reason == "" {
					reason = job.Status
				}
				return "", fmt.Errorf("%w: %s", ErrJobFailed, reason)
			}
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}

	logs, err := c.GetLogs(ctx, jobID)
	if err != nil {
		return "", fmt.Errorf("aiclient: get logs %s: %w", jobID, err)
	}
	text := extractText(logs.Stdout)
	if text == "" {
		return "", ErrEmptyOutput
	}
	return text, nil
}

// extractText turns a job's raw stdout into the single WhatsApp sentence
// GenerateMessage returns: trims surrounding whitespace, strips common
// markdown/quote framing a model tends to wrap its answer in (code fences,
// leading "> " blockquote markers, wrapping double/curly quotes, a leading
// bullet/dash), takes the first non-empty line (a multi-line answer is
// treated as the model over-explaining; only the first line is the
// message), and caps the result at maxOutputChars.
func extractText(stdout string) string {
	s := strings.TrimSpace(stdout)
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	var firstLine string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		firstLine = line
		break
	}
	firstLine = strings.TrimPrefix(firstLine, "> ")
	firstLine = strings.TrimPrefix(firstLine, "- ")
	firstLine = strings.TrimPrefix(firstLine, "* ")
	firstLine = strings.Trim(firstLine, `"'“”‘’`)
	firstLine = strings.TrimSpace(firstLine)

	if len([]rune(firstLine)) > maxOutputChars {
		r := []rune(firstLine)
		firstLine = string(r[:maxOutputChars])
	}
	return firstLine
}
