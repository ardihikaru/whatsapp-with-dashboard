# Brief — Self Convo: scripted two-party conversations between paired sessions

**Status:** implemented 2026-09-09 (this brief was written alongside the code,
not before it). **Written:** 2026-09-09.
**For:** the `senior-fullstack-htmx-go` agent, per this repo's CLAUDE.md, and
for whoever next has to touch the runner.

---

## Why this exists

The operator runs several paired WhatsApp sessions through this gateway. A
session that only ever sends outbound notifications looks, to WhatsApp, like
a machine. The operator wants a way to make two of the fleet's own numbers
exchange a short, natural-looking chat on demand: tick which connected
sessions may take part, pick a saved script, press **Trigger** once, and the
exchange plays out by itself over minutes with human-looking pauses.

The sidebar item is **Self Convo** (`/self-convo`). Scripts are user-supplied
JSON; every upload is kept as a template with its own management page
(`/self-convo/templates`); the shipped example is downloadable from the
trigger page.

**Real paraphrasing offline is not feasible.** The request asked for a pure-Go
function that turns one script into a *different* conversation without any
LLM. There is no Go-native model or synonym toolkit that produces natural
Indonesian without meaning drift, and naive synonym swapping reads as machine
text, which defeats the purpose. What ships instead is **author-declared
variation** (`internal/selfconvo/vary.go`): per-turn `alternatives`, `{{var}}`
placeholders drawn once per run from `vars`, and `optional` turns dropped by
probability. Pacing is randomised too. That is the honest ceiling for "offline
by code".

## The hazard this feature carries

`WSM_SELF_CONVO_ENABLED` defaults to **false**. Two of this deployment's own
paired identities chatting automatically is exactly the traffic pattern that
gets a device flagged, and neither identity has a second copy anywhere (see
CLAUDE.md's singleton-volume entries). The page renders and explains; the
Trigger and Cancel buttons return an in-partial error until an operator opts
in. Template management is deliberately NOT gated — storing JSON is harmless.

## The scheduler decision

"Trigger once and the scenario runs itself" needs a scheduler. The design is a
**persisted state machine polled by one goroutine**, not a timer and not a
cron library:

- `Trigger` expands the script into concrete turn rows *now* (speaker session,
  recipient phone, final text after variation, drawn delay) and inserts a run
  with `state = scheduled`, `next_due_at = start_at`. Delays are drawn at
  expand time and stored per turn, so the runner holds no randomness and the
  "next fire" column on the page is exact.
- The runner (`internal/selfconvo/runner.go`) polls
  `state IN ('scheduled','running') AND next_due_at <= now`, advances **one
  turn per due run per tick**, then sleeps until the earliest `next_due_at`
  capped by the poll interval. Due runs from one tick's `DueRuns` query are
  fanned out across up to `Options.MaxConcurrentRuns` (default 4) goroutines
  rather than processed strictly sequentially — two due runs over disjoint
  pairs used to serialize behind each other's `gw.Send` call and silently
  stretch the pacing this section promises; `Tick` still waits for the whole
  batch before returning, so "one turn per due run per tick" and "a run is
  never processed by two goroutines at once" both still hold. A buffered-1
  `poke` channel (the `internal/wa/inbound.go` shape) lets a trigger or
  cancel wake it early. That sleep is floored at `Options.MinSleep` (never
  zero — a due run whose
  advance fails partway, e.g. a read-only MySQL failover, would otherwise
  leave `next_due_at` stuck in the past and spin the loop at CPU speed) and
  backs off exponentially (`MinSleep`, 2x, 4x, ... capped at the poll
  interval) across consecutive ticks that logged a repo error, resetting on
  the next clean one.
- **Lazy next-due**: after a turn's terminal result, `next_due_at = time the
  send finished + next turn's delay`, never `previous due + delay`. An outage
  of any length yields exactly one immediate send on recovery, then normal
  pacing — no catch-up burst. Pinned by
  `TestNextDueIsComputedFromTheSendTimeSoAnOutageNeverCausesACatchUpBurst`.
- Each send goes through `wa.Manager.Dispatch` (`internal/wa/dispatch.go`),
  the tail of the HTTP handlers' own send path moved down one layer, so the
  per-session token bucket, the sender's retries, and the `wa_messages` audit
  row are shared rather than duplicated. The runner blocks on the terminal
  result (no 15 s cap; `senderLoop` bounds it), so turn N+1 is never sent
  before turn N is resolved.
- **Failure policy**: a send error, or either participant reporting
  disconnected before a turn, fails the run with a reason naming the session
  and skips the pending turns. No speaker swap (it would break continuity)
  and no runner-level retry (the sender already retried).
- **Restart safety** is free for the runs themselves: due rows are simply
  picked up. For a turn found `sending` at boot, `Runner.Recover`
  (`internal/selfconvo/runner.go`) runs ONCE from `Start`, before the poll
  loop begins — not, as an earlier version of this feature did, inline in
  every tick's `processRun`. It reconciles rather than blanket-marking:
  `ClaimTurn` stores the turn's `wa_messages` id BEFORE `Send` is ever
  called, so Recover asks `Gateway.MessageStatus` what that audit row
  actually says — `sent` → the turn is marked `sent` (the send landed; only
  this process's own bookkeeping was interrupted), `failed` → marked
  `failed` with the row's own stored reason, and `queued` or no row at all →
  `interrupted`, the one genuinely unknown case. Either way the run itself
  is left exactly as found (Recover never concludes it done/failed/
  cancelled — that stays the normal Tick cycle's job) and its `next_due_at`
  is set to now, so the very next tick picks it straight back up.
- **Cancel** marks the run cancelled and skips pending turns; the runner
  re-reads state before every send, and `ClaimTurn`'s `state = 'pending'`
  guard (`internal/domain/convo/repository.go`) decides who wins a race with
  it: a cancel landing AFTER the turn is already claimed (sending) lets that
  one turn complete, but a cancel landing BEFORE the claim — between the
  runner picking the turn and marking it sending — wins instead: the claim
  is refused (`ErrTurnNotPending`) and the turn is skipped, never sent.
- **Graceful shutdown** leaves an in-flight send's turn `sending`, never
  `failed` — the send may still be running against its own deadline when
  `Stop()` cuts the runner's own ctx, so its outcome is genuinely unknown;
  the next boot's restart-safety (above) resumes the run exactly as if the
  process had crashed instead.
- Rejected: `time.AfterFunc` per run (dies with the process; a run scheduled
  for tomorrow is lost); cron libraries (recurrence-shaped, and still not
  restart-safe without a store; none in `go.mod`); per-run goroutines (same
  design plus N goroutines and a re-spawn-on-boot step).

The app is a hard singleton (CLAUDE.md), so the runner needs no distributed
lock.

## Schema — and BOTH copies of it

`migrations/0007_self_convo.sql` (goose, MySQL) and the twin in
`internal/db.EnsureSchema`. Three tables:

- `wa_convo_templates` — every uploaded script, raw JSON as an opaque `TEXT`
  blob (the repo has no JSON columns; this is the same shape as
  `wa_inbound_messages.body`).
- `wa_convo_runs` — one row per trigger: the chosen pair (ids, names and
  phones snapshotted so a deleted session still renders), state, progress,
  `start_at`, `next_due_at` (the only column the runner polls, indexed as
  `idx_wa_convo_runs_due (state, next_due_at)`), error, timestamps.
- `wa_convo_turns` — one row per line of the conversation: sender session,
  recipient phone, body, drawn delay, state, the `wa_messages` id, `sent_at`.
  This is the conversation history. Cascade-deleted with its run.

Every send also lands in `wa_messages` with `category = 'self_convo'` and
`client_ref = <run id>`, so the Message Log filter shows a whole conversation
by run. `message.NormalizeCategory` does NOT accept that value from callers —
only the gateway adapter stamps it — so an API client cannot masquerade as a
self-convo.

Run states: `scheduled | running | done | failed | cancelled`. Turn states:
`pending | sending | sent | failed | interrupted | skipped`.

## Script format (v1)

```json
{
  "version": 1,
  "name": "Contoh: rencana akhir pekan",
  "pacing": { "min_seconds": 20, "max_seconds": 90 },
  "vars": { "place": ["Bandung", "Bogor"] },
  "turns": [
    { "speaker": "a", "text": "Halo, sibuk gak Sabtu?" },
    { "speaker": "b", "text": "Enggak, kenapa?", "alternatives": ["Lagi santai, ada apa?"], "delay_seconds": 15 },
    { "speaker": "a", "text": "Mau ke {{place}} gak?", "optional": true, "optional_probability": 0.7 },
    { "speaker": "b", "text": "Boleh, jam berapa?" }
  ]
}
```

Rules live in `internal/selfconvo/script.go`: 2..500 turns, speakers `a`/`b`
only and both must appear, text ≤ 4096 runes (the same limit as `/send`),
pacing `1 ≤ min ≤ max ≤ 3600`, per-turn `delay_seconds` overrides the draw,
turn 1 always fires at `start_at`. Unknown fields are rejected. Speakers are
roles: at trigger time two distinct connected sessions are drawn from the
ticked list and assigned `a`/`b` at random.

## Tests — red first, per this repo's rule 1

Every step landed with a test observed failing before the change. The ones
worth knowing by name when something regresses:

- Scheduler properties: `TestATurnIsNotSentBeforeItsStartAt`,
  `TestNextDueIsComputedFromTheSendTimeSoAnOutageNeverCausesACatchUpBurst`,
  `TestAFreshRunnerPicksUpADueRunLeftByAPreviousProcess`,
  `TestATurnFoundMidSendAfterARestartIsMarkedInterruptedAndTheRunContinues`,
  `TestARunFailsWithTheSessionNameWhenAParticipantDisconnectsMidRun`,
  `TestCancelStopsFurtherSendsAndSkipsThePendingTurns` (all
  `internal/selfconvo/runner_test.go`).
- Send path: `TestDispatchInsertsTheAuditRowBeforeEnqueueingAndReturnsTheTerminalResult`
  and siblings in `internal/wa/dispatch_test.go`; the whole
  `internal/handler` suite is the regression net for the refactor of
  `dispatchSend`.
- Schema parity: `TestEveryMigrationTableExistsInTheSQLiteMirror`.
- Gating and pages: `TestSelfConvoPageRendersTheDisabledNoticeByDefault`,
  `TestTriggeringASelfConvoIsRefusedInThePartialWhenTheFeatureIsDisabled`,
  `TestStartAtIsInterpretedInTheBrowsersOffsetNotUTC`.

Handler-package tests cannot fabricate a connected session
(`wa.Snapshot.Connected` is computed from an unexported field), which is why
the runner depends on a narrow `Gateway` interface and its positive path is
proven against a fake; the handler tests pin only the refusal paths.

## Deploying it

1. Migrate production MySQL **only as part of a real deploy**
   (`make migrate-up`); never from a dev box, per CLAUDE.md.
2. Set `WSM_SELF_CONVO_ENABLED=true` in `.env` and restart the app. The boot
   log says either `self-convo: disabled …` or `self-convo runner started`.
3. Two sessions must be connected; the trigger page lists only those.

### How to know it worked

- The run row on `/self-convo` goes `scheduled → running → done`, progress
  advances, and both handsets show the exchange.
- `/logs?category=self_convo` lists every turn with `client_ref = <run id>`;
  `wa_inbound_messages` shows the other half of each exchange with the same
  ref, because the receiving session's inbound pump stores it.
- Restarting the app mid-run produces one immediate send on recovery and
  normal pacing after — the lazy next-due claim, observed rather than assumed.
