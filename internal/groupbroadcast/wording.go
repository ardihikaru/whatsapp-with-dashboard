package groupbroadcast

import (
	"context"
	"fmt"
	"math/rand/v2"

	"github.com/ardihikaru/whatsapp-with-dashboard/internal/aiclient"
	gb "github.com/ardihikaru/whatsapp-with-dashboard/internal/domain/groupbroadcast"
	"github.com/ardihikaru/whatsapp-with-dashboard/internal/selfconvo/paraphrase"
)

// wordingInput is what one turn's wording depends on — resolved by the
// planner BEFORE calling generateWording (role/language/lateness/reason are
// all drawn first; see planner.go).
type wordingInput struct {
	Role       string // gb.RoleSender | gb.RoleReceiver
	Language   string // gb.LangID | gb.LangJV
	IsLate     bool
	LateReason string // a key from lateReasons (lateness.go), only meaningful when IsLate
	GroupName  string
	SenderName string
}

// wordingResult is generateWording's outcome.
type wordingResult struct {
	Body   string
	Source string // gb.SourceAI | gb.SourceFallback
}

// reasonText renders a lateReasons key as a short natural-language phrase in
// the given language — the seed a fallback template (or the AI prompt)
// builds its excuse around.
func reasonText(key, language string) string {
	id := map[string]string{
		"macet":          "kena macet parah di jalan",
		"ban_bocor":      "ban motor bocor tadi",
		"anak_sakit":     "anak lagi kurang sehat",
		"hujan_deras":    "hujan deras banget dari pagi",
		"antre_bensin":   "antre lama di pom bensin",
		"kesiangan":      "kesiangan bangun",
		"internet_lemot": "internet di rumah lemot banget",
	}
	jv := map[string]string{
		"macet":          "kena macet sing rame banget",
		"ban_bocor":      "ban motor bocor mau",
		"anak_sakit":     "anak lagi rada kurang sehat",
		"hujan_deras":    "udan deres wiwit esuk",
		"antre_bensin":   "ngantri suwe ing pom bensin",
		"kesiangan":      "tangi kawanen",
		"internet_lemot": "internet ing omah alon banget",
	}
	if language == gb.LangJV {
		if s, ok := jv[key]; ok {
			return s
		}
		return "ana kendala sekedhik"
	}
	if s, ok := id[key]; ok {
		return s
	}
	return "ada kendala kecil"
}

// promptForTurn composes the FULL instruction sent to ai-codex-cli. It
// explicitly tells the model to reply with ONLY the final WhatsApp
// sentence, in the requested language/register, nothing else — that
// instruction is what makes aiclient.extractText's framing-stripping
// reliable (see that function's own doc comment).
func promptForTurn(in wordingInput) string {
	lang := "Bahasa Indonesia yang santai dan hangat"
	if in.Language == gb.LangJV {
		lang = "Bahasa Jawa ngoko yang santai dan akrab"
	}

	var ask string
	switch {
	case in.Role == gb.RoleSender:
		ask = fmt.Sprintf("Tuliskan SATU kalimat pembuka WhatsApp yang hangat untuk grup %q, "+
			"menyapa selamat pagi dan mengajak semangat memulai hari.", in.GroupName)
	case in.IsLate:
		ask = fmt.Sprintf("Tuliskan SATU kalimat balasan WhatsApp singkat untuk grup %q dari seseorang yang "+
			"membalas agak telat pagi ini karena %s, menyapa pagi dan meminta maklum sedikit.",
			in.GroupName, reasonText(in.LateReason, in.Language))
	default:
		ask = fmt.Sprintf("Tuliskan SATU kalimat balasan WhatsApp singkat untuk grup %q, "+
			"membalas sapaan pagi dari %s dengan hangat dan ringkas.", in.GroupName, in.SenderName)
	}

	return ask + " Balas HANYA dengan satu kalimat pesan WhatsApp final dalam " + lang +
		", tanpa tanda kutip, tanpa penjelasan tambahan, tanpa markdown."
}

// generateWording resolves one turn's body. It tries the AI client first
// (bounded by ctx's own deadline — the caller, planner.go's PlanDay, wraps
// ctx in context.WithTimeout(WSM_GROUP_BROADCAST_AI_DEADLINE) before calling
// this), and on ANY failure (nil client, submit error, timeout, empty
// output) falls back to a local template + internal/selfconvo/paraphrase
// rewrite, logged at Warn by the caller (planner.go), never here — this
// function has no logger of its own, keeping it a pure(ish) helper the same
// way internal/selfconvo/vary.go's Vary stays free of logging.
func generateWording(ctx context.Context, ai *aiclient.Client, in wordingInput, rng *rand.Rand) wordingResult {
	if ai != nil {
		text, err := aiclient.GenerateMessage(ctx, ai, aiclient.GenerateOptions{Prompt: promptForTurn(in)})
		if err == nil && text != "" {
			return wordingResult{Body: text, Source: gb.SourceAI}
		}
	}
	return wordingResult{Body: fallbackWording(in, rng), Source: gb.SourceFallback}
}

// openerTemplatesID/openerTemplatesJV/replyTemplatesID/replyTemplatesJV/
// lateReplyTemplatesID/lateReplyTemplatesJV are the local template banks
// fallbackWording draws from when the AI call fails — small, hand-written,
// and varied further by paraphrase.Rewrite (Indonesian only; see that
// package's own doc comment on why it has no Javanese lexicon) so a run of
// AI outages still reads as different wording each day, not the exact same
// bytes.
var (
	openerTemplatesID = []string{
		"Selamat pagi tim! Semoga hari ini lancar ya.",
		"Pagi semuanya! Yuk semangat mulai hari ini.",
		"Met pagi tim, semangat kerja hari ini!",
	}
	openerTemplatesJV = []string{
		"Sugeng enjing tim! Mugi dinten iki lancar.",
		"Sugeng enjing sedaya, monggo padha semangat.",
		"Enjing tim, sugeng makarya dinten iki!",
	}
	replyTemplatesID = []string{
		"Pagi juga tim! Semangat.",
		"Siap, pagi semuanya!",
		"Selamat pagi juga, semangat hari ini!",
	}
	replyTemplatesJV = []string{
		"Sugeng enjing ugi tim!",
		"Nggih, sugeng enjing sedaya.",
		"Enjing ugi, monggo semangat!",
	}
	lateReplyTemplatesID = "Maaf tim, telat balas — %s. Pagi semuanya!"
	lateReplyTemplatesJV = "Nyuwun pangapunten telat mangsuli, %s. Sugeng enjing!"
)

func pick(rng *rand.Rand, items []string) string {
	return items[rng.IntN(len(items))]
}

// fallbackWording draws a base template for in.Role/in.Language/in.IsLate,
// fills in a late reason where relevant, and — for Indonesian only — runs
// it through paraphrase.Rewrite with a freshly-drawn register so a run of
// fallbacks still varies day to day (see this file's own doc comment on the
// template banks above). Javanese fallbacks are used as drawn: this
// codebase's paraphrase engine has no Javanese lexicon (see
// internal/selfconvo/paraphrase's package doc), so variety there comes from
// the template bank's own size plus which one is drawn each time.
func fallbackWording(in wordingInput, rng *rand.Rand) string {
	var base string
	switch {
	case in.Role == gb.RoleSender:
		if in.Language == gb.LangJV {
			base = pick(rng, openerTemplatesJV)
		} else {
			base = pick(rng, openerTemplatesID)
		}
	case in.IsLate:
		reason := reasonText(in.LateReason, in.Language)
		if in.Language == gb.LangJV {
			base = fmt.Sprintf(lateReplyTemplatesJV, reason)
		} else {
			base = fmt.Sprintf(lateReplyTemplatesID, reason)
		}
	default:
		if in.Language == gb.LangJV {
			base = pick(rng, replyTemplatesJV)
		} else {
			base = pick(rng, replyTemplatesID)
		}
	}

	if in.Language != gb.LangID {
		return base
	}
	register := paraphrase.DrawRegister(rng)
	rewritten := paraphrase.Rewrite(base, paraphrase.Options{Register: register, MaxRunes: 500}, rng)
	if rewritten == "" {
		return base
	}
	return rewritten
}
