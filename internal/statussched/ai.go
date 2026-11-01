package statussched

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/aiclient"
	sched "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/statusschedule"
)

// DefaultAIDeadline bounds ONE text generation. ai-codex-cli jobs are async
// and take many seconds, so the call is made at PLANNING time (never when a
// post is due) under this deadline; it matches
// config.DefaultGroupBroadcastAIDeadline, whose value cmd/app passes in.
const DefaultAIDeadline = 90 * time.Second

// TextGenerator writes one status text from a prompt. It is an interface so
// tests never need a live ai-codex-cli, and so a nil generator is an
// explicit, testable state ("AI is not configured").
type TextGenerator interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

// ClientGenerator adapts internal/aiclient to TextGenerator.
type ClientGenerator struct {
	Client *aiclient.Client
	Model  string // "" lets the server choose
}

// Generate submits prompt and returns the single line aiclient extracts.
func (g ClientGenerator) Generate(ctx context.Context, prompt string) (string, error) {
	return aiclient.GenerateMessage(ctx, g.Client, aiclient.GenerateOptions{Prompt: prompt, Model: g.Model})
}

// errAINotConfigured is the plan-time reason when no generator was wired.
const errAINotConfigured = "AI is not configured on this deployment"

// buildPrompt composes the FULL instruction for one post. The "reply with
// ONLY the final text" sentence is what makes aiclient's framing-stripping
// reliable (same contract as groupbroadcast/wording.go). The model is told
// to write in the topic's own language, so the schedule needs no language
// field. lastText, when non-empty, is the previous AI post: quoting it is
// how consecutive days are kept from repeating each other.
func buildPrompt(topic, kind, lastText string) string {
	what := "a WhatsApp status update"
	if kind != sched.KindText {
		what = "the caption for a WhatsApp status " + kind
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Write %s about the topic below. ", what)
	b.WriteString("Write it in the SAME LANGUAGE as the topic. ")
	b.WriteString("Make it a single short message of one or two sentences, sized for a WhatsApp status, ")
	b.WriteString("friendly and natural, with at most one or two emoji and no hashtags.\n\n")
	fmt.Fprintf(&b, "Topic: %s\n", topic)
	if strings.TrimSpace(lastText) != "" {
		fmt.Fprintf(&b, "\nYesterday's message was: %s\nDo not repeat or closely paraphrase that message; say something fresh about the topic.\n", lastText)
	}
	b.WriteString("\nReply with ONLY the final message text: no quotes, no markdown, no preface, no explanation.")
	return b.String()
}

// planText resolves the wording for sc's next slot. It returns the text and
// its source, or a non-nil error when nothing postable exists (generation
// failed AND the operator gave no static fallback). A failed generation with
// a fallback is NOT an error: the static text goes out, labelled 'static'.
func (s *Scheduler) planText(ctx context.Context, sc sched.Schedule) (text, source string, err error) {
	fallback := sc.Text
	if sc.Kind != sched.KindText {
		fallback = sc.Caption
	}
	fallback = strings.TrimSpace(fallback)

	var genErr error
	if s.ai == nil {
		genErr = fmt.Errorf("%s", errAINotConfigured)
	} else {
		gctx, cancel := context.WithTimeout(ctx, s.aiDeadline)
		var out string
		out, genErr = s.ai.Generate(gctx, buildPrompt(sc.AITopic, sc.Kind, sc.LastAIText))
		cancel()
		if genErr == nil {
			if out = strings.TrimSpace(out); out != "" {
				return out, sched.TextSourceAI, nil
			}
			genErr = fmt.Errorf("the model returned no text")
		}
	}
	s.log.Warn("statussched: AI generation failed", "schedule", sc.ID, "err", genErr, "fallback", fallback != "")
	if fallback != "" {
		return fallback, sched.TextSourceStatic, nil
	}
	return "", "", fmt.Errorf("AI generation failed: %v", genErr)
}
