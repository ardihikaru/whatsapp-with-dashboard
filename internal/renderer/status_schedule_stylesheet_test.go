package renderer_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	wsm "github.com/ardihikaru/whatsapp-with-dashboard"
)

// status-schedule.css is the FIFTEENTH stylesheet and is global (linked from
// both head templates, like status.css), so the login.html duplication rule
// applies: a stylesheet change that lands in only one <head> is the usual
// way this codebase breaks.

func TestEveryHeadTemplateLinksTheStatusScheduleStylesheet(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			if !strings.Contains(src, "/static/css/status-schedule.css") {
				t.Errorf("%s owns a <head> but does not link status-schedule.css", path)
			}
		})
	}
}

func TestStatusScheduleStylesheetIsLinkedAfterStatusCSS(t *testing.T) {
	for path, src := range headTemplates(t) {
		t.Run(path, func(t *testing.T) {
			status := strings.Index(src, "/static/css/status.css")
			sched := strings.Index(src, "/static/css/status-schedule.css")
			if status < 0 || sched < 0 {
				t.Fatalf("%s: status.css at %d, status-schedule.css at %d — both must be linked", path, status, sched)
			}
			if sched < status {
				t.Errorf("%s links status-schedule.css before status.css; it reuses and overrides status.css's classes, so it must come after", path)
			}
		})
	}
}

func TestBaseLayoutLinksTheStatusScheduleScript(t *testing.T) {
	b, err := fs.ReadFile(wsm.FS, "web/templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "/static/js/status-schedule.js") {
		t.Error("base.html does not load status-schedule.js; the live summary line would never update")
	}
}

func TestStatusScheduleAssetsExistInTheEmbeddedFS(t *testing.T) {
	for _, p := range []string{"web/static/css/status-schedule.css", "web/static/js/status-schedule.js"} {
		if _, err := fs.ReadFile(wsm.FS, p); err != nil {
			t.Errorf("%s is linked but missing from the embedded FS: %v", p, err)
		}
	}
}

func statusScheduleCSS(t *testing.T) string {
	t.Helper()
	b, err := fs.ReadFile(wsm.FS, "web/static/css/status-schedule.css")
	if err != nil {
		t.Fatal(err)
	}
	return cssCommentRE.ReplaceAllString(string(b), "")
}

// TestStatusScheduleTokensAllHaveADarkCounterpart: every --ss-* custom
// property declared in :root must be redeclared under [data-theme="dark"],
// or that colour is light-theme-only.
func TestStatusScheduleTokensAllHaveADarkCounterpart(t *testing.T) {
	css := statusScheduleCSS(t)
	block := func(selector string) string {
		i := strings.Index(css, selector+" {")
		if i < 0 {
			t.Fatalf("no %s block", selector)
		}
		j := strings.Index(css[i:], "}")
		return css[i : i+j]
	}
	decl := regexp.MustCompile(`--ss-[a-z-]+:`)
	light, dark := block(":root"), block(`[data-theme="dark"]`)
	for _, name := range decl.FindAllString(light, -1) {
		if name == "--ss-ease:" {
			continue // motion, not a colour
		}
		if !strings.Contains(dark, name) {
			t.Errorf("%s is declared in :root but has no [data-theme=\"dark\"] counterpart", strings.TrimSuffix(name, ":"))
		}
	}
}

// TestStatusScheduleRulesUseNoRawColours: a literal colour inside a rule
// (rather than a token) cannot follow the theme.
func TestStatusScheduleRulesUseNoRawColours(t *testing.T) {
	css := statusScheduleCSS(t)
	// Drop the two token blocks; everything left is rules.
	rules := regexp.MustCompile(`(?s)(:root|\[data-theme="dark"\])\s*\{[^}]*\}`).ReplaceAllString(css, "")
	if m := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|rgba?\(`).FindString(rules); m != "" {
		t.Errorf("a rule uses the raw colour %q; use a var(--…) token", m)
	}
}

// TestStatusScheduleMotionFollowsTheHouseRules pins the Emil Kowalski rules
// this stylesheet was written to: custom ease-out, named transition
// properties (never `all`), 0.97 press feedback in 160ms, hover behind a
// real-hover media query, and a reduced-motion block.
func TestStatusScheduleMotionFollowsTheHouseRules(t *testing.T) {
	css := statusScheduleCSS(t)
	for _, c := range []struct{ name, needle string }{
		{"custom ease-out curve", "cubic-bezier(0.23, 1, 0.32, 1)"},
		{"press feedback", "scale(0.97)"},
		{"160ms press duration", "transform 160ms"},
		{"180ms panel switch", "180ms var(--ss-ease)"},
		{"@starting-style row entry", "@starting-style"},
		{"200ms row entry", "opacity 200ms"},
		{"faster 120ms row exit", "opacity 120ms"},
		{"hover gated to real hover", "@media (hover: hover) and (pointer: fine)"},
		{"reduced motion", "@media (prefers-reduced-motion: reduce)"},
	} {
		if !strings.Contains(css, c.needle) {
			t.Errorf("status-schedule.css lacks %s (%q)", c.name, c.needle)
		}
	}
	if m := regexp.MustCompile(`transition:\s*all\b`).FindString(css); m != "" {
		t.Errorf("a rule uses %q; transitions must name their properties", m)
	}
	if strings.Contains(css, "ease-in ") || strings.Contains(css, "ease-in,") || strings.Contains(css, "ease-in;") {
		t.Error("a rule uses ease-in; entering/pressing elements use the custom ease-out")
	}
	// Every :hover rule must sit inside the hover media query.
	hover := regexp.MustCompile(`(?s)@media \(hover: hover\) and \(pointer: fine\)\s*\{.*?\n\}`).ReplaceAllString(css, "")
	if strings.Contains(hover, ":hover") {
		t.Error("a :hover rule sits outside @media (hover: hover) and (pointer: fine)")
	}
}

// TestStatusScheduleAISwitchFollowsTheMotionAndAccessibilityRules pins the
// AI switch / topic reveal: 160ms press + slide, the 180ms kind-panel reveal
// recipe, a hidden topic that is display:none (out of the tab order, no
// height to animate), an opacity-only reveal under reduced motion, hover
// behind the real-hover query, and a counter that never animates.
func TestStatusScheduleAISwitchFollowsTheMotionAndAccessibilityRules(t *testing.T) {
	css := statusScheduleCSS(t)
	for _, c := range []struct{ name, needle string }{
		{"switch press feedback", ".ss-switch:active { transform: scale(0.97); }"},
		{"switch 160ms press", ".ss-switch {"},
		{"thumb slides with transform in 160ms", "transform 160ms var(--ss-ease)"},
		{"track recolours in 160ms", "background-color 160ms var(--ss-ease)"},
		{"topic hidden state is display:none", ".ss-ai-topic { display: none;"},
		{"topic reveals with the 180ms panel recipe", "animation: ss-panel-in 180ms var(--ss-ease)"},
		{"reduced motion keeps an opacity-only reveal", "animation: ss-fade-in 120ms linear"},
		{"reduced motion stops the thumb sliding", ".ss-switch-thumb { transition: none; }"},
		{"fallback labels swap by sibling selector", ".ss-ai-input:checked ~ [data-ss-panel=\"text\"] .ss-lbl-ai"},
		{"switch hover is gated", ".ss-switch:hover .ss-switch-track"},
	} {
		if !strings.Contains(css, c.needle) {
			t.Errorf("status-schedule.css lacks %s (%q)", c.name, c.needle)
		}
	}
	// ss-fade-in must really be opacity only.
	if m := regexp.MustCompile(`(?s)@keyframes ss-fade-in\s*\{(.*?)\n\}`).FindStringSubmatch(css); m == nil || strings.Contains(m[1], "transform") {
		t.Errorf("ss-fade-in keyframes missing or moving: %v", m)
	}
	// Never animate height or layout for the reveal, and the counter is static.
	for _, banned := range []string{"transition: height", "animation: ss-grow", "max-height"} {
		if strings.Contains(css, banned) {
			t.Errorf("status-schedule.css uses %q; the topic reveal must not animate height", banned)
		}
	}
	if m := regexp.MustCompile(`\.ss-ai-count[^{]*\{[^}]*(transition|animation)`).FindString(css); m != "" {
		t.Errorf("the topic counter must not animate: %q", m)
	}
}

func TestStatusScheduleScriptMentionsAIWrittenAndCountsTheTopic(t *testing.T) {
	b, err := fs.ReadFile(wsm.FS, "web/static/js/status-schedule.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"AI-written", "data-ss-ai-count", "data-ss-ai-topic", "data-ss-ai"} {
		if !strings.Contains(src, want) {
			t.Errorf("status-schedule.js lacks %q", want)
		}
	}
}
