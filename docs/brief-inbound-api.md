# Brief — `GET /api/v1/inbound`: let a consumer read customer replies

**Status:** not implemented. **Written:** 2026-08-31.
**For:** the `senior-fullstack-htmx-go` agent, per this repo's CLAUDE.md.

---

## Why this exists

A consumer product (Clino / `laundry-saas-v1`) asks laundry customers on WhatsApp
whether they received their order, and settles the order on the reply. Its cron job
polls this gateway every two minutes and has been getting, on production, since the
feature shipped:

```
WARN confirm-receipts: reply poll failed, continuing to the sweep
     error="wsmclient: list inbound: status 404: 404 page not found"
```

Observed 2026-08-31 on `clino-prod-jobs-1`, once every 2 minutes, on every run.
The consumer reaches this gateway at `WSM_URL=http://10.10.0.4:18801` with HTTP
Basic as API client `ClinoProd`.

The consumer degrades honestly — a 4-hour timer closes the order as
`auto_timeout` — so **nothing is broken today, but a customer's own word is
never heard.** "The system assumed" and "she said she got it" are two different
claims, and only one of them is currently obtainable.

### Why the endpoint does not exist here

It was written — in the consumer's own monorepo, for that product's *local*
gateway (`apps/whatsapp-session-management`), which was then **retired on
2026-08-29** in favour of this central one. The reader half of the feature was
built into a component that stopped being deployed ten days later. That is the
whole story; there is no subtle bug to find.

**Use that implementation as the reference.** It is complete, tested and
production-shaped. Paths below are relative to
`~/devel/projects/ardihikaru/my-projects/yuza-ai-gaming-workshop/apps/laundry-saas-v1/`.

---

## The wire contract is NOT negotiable

The consumer's client is already deployed and pinned. Match it exactly:
`pkg/wsmclient/inbound.go`.

```
GET /api/v1/inbound?since=<RFC3339>&limit=<int>
Authorization: Basic <login_id:secret>        # the /api/v1 surface, never a cookie
```

- `since` is **required**. The reference answers `400 "since is required (RFC3339)"`
  when absent and `400 "since must be RFC3339"` when unparseable. Required rather
  than defaulted so a caller that forgot it fails loudly instead of quietly reading
  the whole table.
- `limit` optional; clamp to a max (reference: 500, default 200 when absent or out
  of range). The consumer sends 500.
- **200** → a JSON **array**, oldest first:

```json
[{"id":"...","from_phone":"6285...","body":"sudah",
  "received_at":"2026-08-31T05:14:00Z","client_ref":"01K...","tenant_ref":"01K..."}]
```

Field names verbatim — `pkg/wsmclient/inbound.go:22-33`. Any non-200 is an error to
the consumer; it does not retry within a run.

**Oldest first is behavioural, not cosmetic.** The consumer resolves a reply against
the open questions it answers; replaying out of order lets a later message close a
question an earlier one already answered.

Reference handler: `apps/whatsapp-session-management/internal/handler/api_inbound.go:33-73`,
routed at `routes_api.go:60`.

---

## The tier stamp is the security property

One gateway serves several products and several environments. The consumer **must
ignore** any row whose `client_ref` it cannot resolve in its own database, and it
does. That only works if this gateway stamps honestly:

> `client_ref` / `tenant_ref` are copied from the **most recent outbound message to
> that same phone number**, within a 24-hour window. Un-stamped (`NULL`) is the SAFE
> outcome — no product claims the message.

The 24 hours is chosen against the question being asked (a 4-hour deadline) and
against a customer messaging out of the blue months later being silently attributed
to an old order.

**The outbound side of this already exists here.** `migrations/0002_message_attribution.sql`
put `api_client_id`, `client_ref`, `tenant_ref`, `category` on `wa_messages` and
indexed `(client_ref, created_at DESC)`. The lookup is one query against
`wa_messages` by `to_phone`, newest first, within the window.

Reference: `apps/whatsapp-session-management/internal/wa/inbound.go:22-29,67-73`.

---

## Four pieces of work

### 1. Schema — and BOTH copies of it

MySQL migration `0006_inbound_messages.sql`, and the SQLite mirror in
`internal/db.EnsureSchema`, **in the same commit** (CLAUDE.md: "Two hand-maintained
copies of one schema… A migration added to only one of them makes every
SQLite-backed test pass against a schema production does not have").

The reference DDL, which already carries the right indexes:

```sql
CREATE TABLE wa_inbound_messages (
  id            VARCHAR(128) NOT NULL PRIMARY KEY,
  session_id    VARCHAR(64)  NOT NULL,
  from_phone    VARCHAR(32)  NOT NULL COMMENT 'normalized digits',
  from_jid      VARCHAR(128) NOT NULL,
  body          TEXT         NULL,
  received_at   VARCHAR(40)  NOT NULL COMMENT 'RFC3339, from the WhatsApp envelope not our clock',
  client_ref    VARCHAR(64)  NULL,
  tenant_ref    VARCHAR(64)  NULL,
  replies_to_message_id VARCHAR(128) NULL COMMENT 'wa_messages.id the stamp came from',
  created_at    VARCHAR(40)  NOT NULL,
  KEY idx_wa_inbound_received (received_at),
  KEY idx_wa_inbound_from (from_phone, received_at),
  KEY idx_wa_inbound_client (client_ref, received_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
```

`received_at` comes from the **WhatsApp envelope**, not from `time.Now()` — the
consumer's `since` window is only meaningful if it measures when the customer sent,
not when we happened to persist. `INSERT IGNORE` on the primary key makes redelivery
free, which matters because the event stream is lossy by design.

Reference: `apps/whatsapp-session-management/migrations/0003_inbound.sql`,
repository at `internal/domain/inbound/repository.go:52-95`.

### 2. Consume the `message` event

This is the only piece with **no reference to port**, because the two gateways have
different transports. The consumer's WSM ran whatsmeow in-process and handled
`*events.Message` off the socket loop. This gateway runs waxum and already consumes
its fleet-wide SSE stream: `internal/waxum.Client.TailEvents`
(`GET /api/v1/events/tail`, filterable by event name), driven from
`internal/wa/backend_waxum_events.go`. `"message"` is a valid waxum event name —
`internal/handler/api_webhooks.go:24-37` already validates it as one.

**⚠ UNVERIFIED, and it is the first thing to establish:** the shape of waxum's
`message` event payload — whether it carries the sender JID, the text, the envelope
timestamp and an `is_from_me` flag, and whether it fires for incoming messages only
or also echoes our own sends. `ardihikaru/waxum` is not cloned on the machine this
brief was written on. Read the fork's handler, or observe one real event, **before**
designing the mapping. Do not infer it from this document.

**Respect the rule that file states about itself:** an SSE event must not write
session state. Persisting an inbound message is not session state, so this is
compatible — but the new path must not call `applyStatus` and must not poke
`statusPump`. Keep it off that code path entirely.

Three kinds must be **dropped before the database**, and each would be a bug
downstream rather than mere noise (`internal/wa/inbound.go:33-53`):

- **our own messages echoed back** (`IsFromMe`) — storing them lets the consumer's
  matcher read a question as its own answer;
- **status broadcasts** — not addressed to us;
- **group chats** — every question this gateway asks is asked of one person, so a
  group reply has no single sender to attribute.

Also drop empty bodies (a sticker, an image, a reaction): nothing to parse, and it
only grows a table nobody reads.

Text lives in **two** shapes and missing the second drops exactly the replies that
matter most: a bare `Conversation`, and an `ExtendedTextMessage` — which is what a
client sends when the message **quotes** another one, i.e. a customer tapping reply
on our question (`internal/wa/inbound.go:88-96`).

The persist must be best-effort and non-blocking: a reply we could not store is
worth less than a session that stops reading its socket.

### 3. Repository

`internal/domain/inbound` — `Insert` and `Since(ctx, t, limit)` returning oldest
first, plus `LatestOutboundRefs(ctx, phone, notBefore)` for the stamp. Raw
`database/sql`, no ORM, per this repo's layout table.

### 4. Route

`mux.Handle("GET /api/v1/inbound", api(http.HandlerFunc(h.GetAPIInbound)))` — the
`api(...)` wrapper, so a browser cookie is rejected here. "Auth surfaces do not
mix" is a tested invariant in this repo.

Answer **503** (not 500, not 404) when the inbound log is unavailable, so a
consumer can distinguish "this gateway has no inbound support configured" from a
genuine failure — the consumer's client comments already anticipate that code.

---

## Tests — red first, per this repo's rule 1

Each of these must fail when the change is reverted:

1. `since` absent → 400; `since` malformed → 400. (Cheap, and it is the contract.)
2. A stored arrival is returned by `Since` **oldest first** — insert two out of
   order, assert the order out.
3. An arrival whose phone matches a recent outbound row gets that row's
   `client_ref`/`tenant_ref`; one with no outbound inside the window gets **NULL**,
   not a guess. This is the security property — test both directions.
4. An outbound row older than the 24-hour window does **not** stamp.
5. `IsFromMe`, group and status-broadcast events are dropped — assert no row.
6. An `ExtendedTextMessage` (quoted reply) yields its text, not an empty body.
7. A browser cookie is rejected on `/api/v1/inbound`; Basic auth is accepted.
8. Re-delivering the same message id inserts one row, not two.

The consumer side is already covered on its own side of the wire
(`apps/core-api/internal/service/confirm_receipt_match_test.go`) and needs nothing
from here.

---

## Deploying it

This gateway is **shared with other products** and its WhatsApp session is a hard
singleton — no replicas, no rolling restart, and never `docker compose down -v`
(the volume is the paired device's identity). Treat the restart as a short,
scheduled WhatsApp reconnect.

Prod is at `/home/sismedika/devel/projects/whatsapp-with-dashboard` on
`ssh rnd-v2-vpn`, compose project `whatsapp-with-dashboard`, published on
`10.10.0.4:18801`. Migration runs via `Dockerfile.migrate` against the **shared**
production MySQL — read this repo's CLAUDE.md hazard entries on that database and
on `docker compose config` before touching either.

### How to know it worked

No dashboard needed. On the consumer's box:

```
ssh rnd-v2-vpn 'docker logs clino-prod-jobs-1 --since 10m 2>&1 | grep confirm-receipts'
```

The `WARN … status 404` line stops, and `polled=` starts counting arrivals. A real
end-to-end proof is one WhatsApp reply of "sudah" to a delivered order, which should
move it to `Sudah Diambil` with `confirm_source = customer_reply` — **not**
`auto_timeout`, which is what the 4-hour sweeper writes and is a different claim.
