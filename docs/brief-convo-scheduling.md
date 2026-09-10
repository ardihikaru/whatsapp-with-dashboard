# Brief — Convo Scheduling: recurring, multi-receiver, self-chaining Self Convo

**Status:** implemented across Phases 1-7, this brief written alongside Phase
7 (browser verification and documentation only — no behavioural change in
this phase). **Written:** 2026-09-10.
**For:** the `senior-fullstack-htmx-go` agent, per this repo's CLAUDE.md, and
for whoever next has to touch the planner or the corpus generator.

---

## Why this exists

`docs/brief-self-convo.md` shipped a single operator action: tick two
sessions, pick a script, press **Trigger**, watch one conversation play out
once. That is a lot of manual attention for something meant to look like
background, human-shaped traffic — an operator who has to remember to
trigger a chat every few hours is exactly the kind of routine a script would
otherwise automate away, and a fleet with dozens of paired numbers cannot be
kept "alive-looking" one manual trigger at a time.

Convo Scheduling turns that single action into a standing plan: pick a
sender, tick several receivers, choose a recurrence (daily / weekly /
every-N-days) and a time-of-day window, and the system materialises real
Self Convo runs on its own, at the configured cadence, for as long as an
operator lets it. A finished conversation can also, optionally, schedule its
own follow-up — a "chain" — instead of simply stopping, so a fleet keeps
generating plausible new small-talk without any further attention at all.

None of this replaces the Self Convo runner from the earlier brief. Every
run a schedule creates still goes through the exact same `Runner` (turn
pacing, restart safety, cancel semantics, the failure policy) — Convo
Scheduling only adds a second, separate background loop (`Planner`) whose
entire job is deciding *when* to call `Runner.StartOccurrence`, never how a
run itself advances.

## The corpus: why generate a catalogue instead of asking operators to write one

A schedule needs a large, varied POOL of templates to draw an opener from —
"pick something to open a `futsal` conversation with" has to mean more than
one hard-coded script, or every fleet's chats about football would read
identically. Hand-authoring hundreds of full conversations does not scale
and does not vary well by hand either.

`internal/selfconvo/corpus` instead treats a topic as a **slot grammar**: a
pack (`internal/selfconvo/corpus/packs/*.json`) declares a topic's arc as
three scenes (opener/middle/closer), each scene as independently-authored
*setups* and *endings*, and `Generate` takes the full cross product of a
scene's setups and endings — four setups by four endings is 16 genuinely
different templates from eight authored fragments, not sixteen authored
scripts. 50 hand-written packs become roughly 2,400 templates
(`cmd/convoseed`), a scale no operator would hand-curate, while every
generated template still passes the same `selfconvo.Validate` an upload
would.

Wording variation is deliberately NOT this package's job. `Generate` produces
one clean way of saying a setup/ending combination; `selfconvo.Vary` and
`internal/selfconvo/paraphrase` (below) vary the WORDING at send time. Mixing
the two concerns — generating more distinct conversations vs. saying an
existing conversation differently — would make it much harder to reason
about which knob to turn when the corpus reads as too repetitive.

## Why the link graph is weighted, cyclic, and depth-capped

Every pack also declares **links** between its own scenes — `opener→middle`,
`middle→closer`, and, in most packs, `closer→opener`. `Generate`'s
`assignLinks` turns each of those into real per-template edges
(`convo.TemplateLink`), weighted, with a random gap window (minutes for a
same-day follow-up turn, days for a "loop back and start fresh" edge).

**Weighted, not uniform**, because a real conversational arc is not a fair
coin flip between "wrap up now" and "keep going" — a schedule's operator
should be able to make the mundane continuation (finish the middle scene)
much more likely than the rarer branch, the same way `assignLinks`' own
`branchFactor` extra edges exist for variety without making every path
equally probable.

**Cyclic on purpose.** A pack's own `closer→opener` link is a real cycle in
the graph: a finished "ngopi" conversation about coffee can, days later,
lead back into a brand-new opener for the *same* topic. This is precisely
what makes an unattended, indefinitely-running schedule keep producing
plausible new small talk instead of exhausting its topic and going silent —
the cycle is the mechanism, not an oversight the graph happens to contain.

**Depth-capped for the same reason a cycle needs one.** `runner.go`'s
`continueChain` refuses to create a successor once `run.ChainDepth + 1 >
Options.MaxChainDepth` (default 20). Without a cap, a schedule with
`ChainEnabled` sitting on a cyclic graph would chain forever by
construction — there is no other terminal condition once the graph loops
back on itself. The cap is not a performance safeguard; it is the only thing
that makes "the graph is cyclic" a feature rather than an infinite loop.
Reaching the cap ends the chain quietly (logged at debug), the same as
reaching a genuine leaf template with no outgoing edges — from the runner's
point of view a capped chain and an exhausted one look identical, which is
deliberate: an operator inspecting a stopped chain does not need to know
which of the two happened to understand that nothing is stuck.

## Why fan-out is one run per receiver, not one run with many recipients

A schedule names a sender and a LIST of receivers. `Runner.StartOccurrence`
creates **one separate `convo.Run` per receiver** — sender always speaker A,
that one receiver always speaker B — rather than one run that somehow
addresses every receiver in a single pass.

This follows directly from what a run already is: two-party, exactly two
speakers (`a`/`b`), because that is the shape of a real WhatsApp
conversation between two numbers. There is no such thing as a three-way
"conversation" in this model, and inventing one would mean a parallel turn
format, a parallel sender/recipient resolution, and a parallel failure
policy just for the multi-receiver case — all to represent something that
is not how two people (or two paired sessions) actually exchange messages.
Per-receiver runs mean every existing piece — `Runner`, the failure policy,
`Runner.Cancel`, the restart-safety recovery path — needs no receiver-count
special case at all; a schedule with five receivers is simply five ordinary
two-party runs that happen to share a sender, a template, and an occurrence
date.

The one place multi-receiver fan-out DOES need its own logic is timing:
`StartOccurrence` jitters each receiver's start inside a distinct,
non-overlapping slice of `[0, Options.ReceiverJitter)` (`jitterReceiverStart`)
so five receivers never all receive their opening message in the same
second — literally the most obviously-automated signal a multi-receiver set
could otherwise emit. Each run also draws its own `ParaphraseSeed`
(`createScheduledRun`), so the same template reads differently to different
receivers even before any per-turn `alternatives`/`optional` variation is
applied.

## Why resume does not back-fill

Pausing (operator-initiated) or stalling (automatic, on a participant going
unhealthy) a schedule leaves `LastPlannedDate` exactly where it was.
`Planner.Resume` (`planner.go`) clears the state back to `active` and
**does nothing else** — it does not walk the missed days and materialise the
occurrences that would have fired while the schedule was down.

The alternative — catching up on every day a schedule was paused or
stalled — would produce exactly the burst of back-dated, all-at-once
conversations this feature exists to avoid. `PlanDay`'s own step 7 already
skips a start time that has fallen into the past *within a single day* for
the identical reason (a start time already behind "now" is skipped, not
fired immediately, so a planner that boots late never fires a stampede of
overdue sets); "resume catches up" would be the same failure at the scale of
days instead of minutes. Concretely: a schedule paused for a week and then
resumed simply waits for its next matching day under its existing
recurrence — nothing for the missed week is ever sent. An operator who wants
different behaviour edits the schedule's dates; the system itself never
infers "catch up" as the right thing to do with automated traffic.

## Why a past-due start time is skipped, not fired

`PlanDay`'s per-schedule algorithm draws `sets_per_day` start times inside
the schedule's own `[window_start, window_end]`, in the schedule's own
timezone, for TODAY. Step 7 of that algorithm: any drawn start time that
has already passed by the time `PlanDay` actually runs is **skipped
entirely**, never fired immediately as a "better late than never"
substitute.

This matters because the planner is a poll loop (`defaultPlannerPollInterval`,
5 minutes), not a precise scheduler — a process restart, a slow poll tick,
or simply the planner catching up on a backlog of schedules can mean `now`
has already moved past a drawn start time before that schedule is even
evaluated. Firing it anyway the moment it is noticed would mean a schedule's
actual send times drift with however loaded the planner happens to be,
producing timing that has nothing to do with the configured window — and,
worse, would let every schedule that missed its slot fire in a lockstep
burst the instant the planner catches up, which reads as machine-generated
traffic exactly as clearly as a resume-triggered catch-up burst would (see
above — this is the same principle applied within one day instead of across
paused days). Skipping the slot means a schedule simply plans one fewer set
today; tomorrow's draw is unaffected and un-degraded by today's miss.

## Why paraphrase is a surface rewriter, and what it cannot do

Every run a schedule creates still goes through `internal/selfconvo/vary.go`
and `internal/selfconvo/paraphrase` exactly as a manually-triggered run
does — Convo Scheduling adds no second wording-variation mechanism. It is
worth restating here because a schedule is precisely the surface where the
limits of that rewriter matter most: an operator manually triggering one
conversation can read the output before it goes out; a schedule cannot,
because nobody is watching every occurrence a recurring plan produces over
weeks.

`paraphrase`'s own package doc is explicit about the boundary: it is "a
pure-Go, offline, deterministic SURFACE paraphrase engine... it does not
understand meaning." What it does — lexicon substitution, greeting/filler/
particle insertion, a small set of defensible clause reorders,
register-gated abbreviation, casing/punctuation jitter, an optional trailing
emoji, optional typo simulation — is mechanical text transformation on top
of a text with placeholders, URLs, numbers and quoted spans carved out and
left untouched. It reads as a different way of saying the same authored
line, never as a different sentence with different content. There is no
Go-native model or synonym toolkit that rewrites Indonesian without meaning
drift, and this package does not attempt that; a corpus author who wants
genuinely different content writes another setup/ending pair, not a hope
that paraphrase will invent one.

The practical consequence for scheduling specifically: a schedule running
for months against the same small number of corpus templates for one topic
will eventually repeat itself in STRUCTURE (the same setup/ending
combinations recur) even though no two sends are byte-identical (paraphrase
plus `Vary`'s alternatives/optional turns/var draws keep the surface text
different every time). Corpus size, not paraphrase strength, is the actual
lever against that — see the corpus section above.

## Layout — the operator UI

`internal/handler/self_convo_schedule_ui.go` follows
`self_convo_ui.go`'s/`self_convo_templates_ui.go`'s established shape
throughout: `convo_schedule_rows.html` is an HTMX partial rendered at 2xx
regardless of outcome (HTMX only swaps a 2xx response), and the multi-field
create/edit form is a plain full-page POST that re-renders itself with
`Form`+`Error` on rejection (`api_clients.go`'s shape, not an HTMX swap —
this form is too large to live inside a table row). No new domain or runner
logic lives in the handler layer: every mutation goes through
`convo.Repository` (CRUD/listing) or `*selfconvo.Planner`
(`Pause`/`Resume`/`Stop_`, already committed in Phase 3) exactly as the
phase brief required.

Two management surfaces are deliberately NOT gated by
`WSM_SELF_CONVO_ENABLED`, matching the same "management works, sending does
not" split `self_convo_templates_ui.go` already established for templates:

- Reviewing, pausing, resuming, stopping or deleting an EXISTING schedule
  stays possible with the flag off — an operator can wind a fleet down
  without first turning automated sending back on. Only reaching `active`
  (create, or resume) sends anything.
- Deleting a schedule can only ever reduce future automated sending, so it
  is never gated either.

## Known limitation: unbounded run/turn growth

**A schedule with `EndDate == ""` (the deliberate "no end date" / unlimited
setting) runs, and writes `wa_convo_runs`/`wa_convo_turns` rows, forever.**
Nothing in this codebase prunes either table today. This is a real,
un-addressed operational gap, not a design decision to leave load-bearing:
an operator who leaves an unlimited schedule running for months will
accumulate an unbounded number of run/turn rows, and the `/self-convo`,
`/self-convo/schedules/{id}` pages' own run tables (`GetSelfConvo`'s
`ListRuns(ctx, 50)`, `self_convo_ui.go`; `selfConvoScheduleRunsLimit`,
`self_convo_schedule_ui.go`) only cap what is DISPLAYED, not what is
STORED. There is no retention job, no archival path, and no operator-facing
warning about table growth anywhere in this phase's work. A future phase
that adds retention should treat this paragraph as the reason it exists,
not as something this phase already handled.

## Schema — and both copies of it

`migrations/0008_convo_scheduling.sql` (goose, MySQL) and the twin in
`internal/db.EnsureSchema`, additive on top of 0007 rather than new tables
where an existing row already had somewhere to hold a new fact:

- `wa_convo_templates` gains `topic`/`stage`/`origin`, so a schedule can draw
  a candidate by topic+stage instead of an operator naming one id, and a
  later corpus regeneration can bulk-replace every `origin='corpus'` row
  without touching an operator's own uploads.
- `wa_convo_runs` gains `schedule_id`/`occurrence_date`/`chain_prev_run_id`/
  `chain_depth`/`topic`/`paraphrase_seed`. `schedule_id` is deliberately NOT
  a foreign key — deleting a schedule must not make its already-finished
  runs un-renderable, the same reasoning `session_a_name`/`phone_a` follow
  in 0007. `occurrence_date` is a `VARCHAR`, not a `DATE`, for the same
  lexical-comparison convention every date column in this schema uses.
- Two new tables: `wa_convo_template_links` (the correlation graph — DOES
  cascade off both endpoints, unlike `wa_convo_runs`'s snapshot style, since
  a link has no independent audit value once either template it connects is
  gone) and `wa_convo_schedules` + `wa_convo_schedule_receivers` (the
  recurring plan itself and its fan-out targets).

## Tests — red first, per this repo's rule 1

Every phase landed with a test observed failing before the change. Worth
knowing by name when something in this area regresses:

- Corpus generation: `TestGenerateIsDeterministicForASeed`,
  `TestGenerateProducesADifferentResultForADifferentSeed`,
  `TestEveryTopicsGraphIsReachableFromAnOpener` (`internal/selfconvo/corpus`).
- Chain/fan-out: `TestNextTemplate...` (weighted draw correctness, `chain.go`),
  the `StartOccurrence`/`jitterReceiverStart` and `continueChain` tests in
  `internal/selfconvo/runner_test.go`, including the max-chain-depth guard.
- Planner: the `scheduleFiresOn`/`drawStartTimes`/past-start-skip tests and
  `Resume`'s no-back-fill behaviour in `internal/selfconvo/planner_test.go`.
- Operator UI: `internal/handler/self_convo_schedule_ui_test.go`,
  `TestTheDeleteConfirmationDoesNotPromiseAnUnlinkTheWaxumBackendCannotDo`'s
  sibling gating tests for schedule mutation while the flag is off.
- Schema parity: `TestEveryMigrationTableExistsInTheSQLiteMirror`.
- Phase 7 (this brief): `TestSeedConvoSchedulingWritesACorpusPackAndTwoSchedulesWithTheStatesThePagesRender`
  (`cmd/e2eseed`) and `e2e/convo-schedules.spec.ts` — see
  `docs/e2e-evidence.md`'s 2026-09-10 entry for the full run, including a
  harness limitation found and worked around rather than hidden (the
  create-schedule form's Sender/Receiver picker has no options in the
  hermetic e2e harness, because it never pairs a real device).

## Deploying it

1. Migrate production MySQL **only as part of a real deploy**
   (`make migrate-up`); never from a dev box, per CLAUDE.md.
2. Materialise the corpus into the target store: `make convo-seed` (wraps
   `cmd/convoseed`; `ARGS="-dry-run"` to preview counts first, `-replace` to
   regenerate without duplicating `origin='corpus'` rows).
3. Set `WSM_SELF_CONVO_ENABLED=true` in `.env` and restart the app — the
   same flag Self Convo itself uses; Convo Scheduling adds no second flag.
4. Create a schedule from `/self-convo/schedules/new`: pick a sender and at
   least one receiver from the connected fleet, a topic the corpus actually
   covers, a recurrence, a daily send window and (optionally) an end date.

### How to know it worked

- The schedule's own page (`/self-convo/schedules/{id}`) shows its recent
  runs table filling in on the next matching day, in the schedule's own
  window.
- A stalled schedule shows the exact session and reason it stalled for,
  in the same place an operator would look to fix it.
- `/logs?category=self_convo` and `wa_inbound_messages` behave exactly as
  documented in `docs/brief-self-convo.md` — Convo Scheduling changes
  nothing about how a run's own messages are logged.
