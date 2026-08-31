# Reading customer replies — `GET /api/v1/inbound`

How another platform consumes inbound WhatsApp messages from this gateway.

This gateway is **shared**. One WhatsApp session serves several products and
several environments, so the single most important rule on this page is the
[tier stamp contract](#the-tier-stamp-contract-read-this-one): *ignore any row
you cannot resolve as yours.* Everything else is mechanics.

---

## The endpoint

```
GET /api/v1/inbound?since=<RFC3339>&limit=<int>
```

| | |
|---|---|
| Auth | `Authorization: Basic base64(login_id:secret)` or `Bearer <token>` |
| Response | `200` + a JSON **array**, **oldest first** |
| Reachability | Private network only — see [Reaching the gateway](#reaching-the-gateway) |

### Query parameters

| Name | Required | Meaning |
|---|---|---|
| `since` | **yes** | RFC3339 instant. Returns messages whose `received_at` is at or after this. |
| `limit` | no | Page size. Max 500, default 200. Values ≤ 0 or > 500 fall back to 200. |

`since` is **required, not defaulted**, on purpose. A default would silently
return either the whole table or an arbitrary window, and a caller that forgot
the parameter would look like it was working right up until the day it mattered.
Omitting it is an error, loudly:

```
400 {"error":"since is required (RFC3339)"}
400 {"error":"since must be RFC3339"}
```

### Response

```json
[
  {
    "id": "3EB0C8F1A2B3C4D5E6",
    "from_phone": "6285132148259",
    "body": "sudah",
    "received_at": "2026-08-31T05:14:00Z",
    "client_ref": "01K7X2M4QY8ZN3V6RB0TCDEFGH",
    "tenant_ref": "01K7X2M4QY8ZN3V6RB0TCDIJKL"
  }
]
```

| Field | Meaning |
|---|---|
| `id` | The WhatsApp message id. Stable, globally unique, and your dedupe key. |
| `from_phone` | Sender's number as normalized digits, no `+`, no separators. May be empty — see [When `from_phone` is empty](#when-from_phone-is-empty). |
| `body` | The message text. Never empty (text-free messages are not stored). |
| `received_at` | RFC3339 UTC, taken from the **WhatsApp envelope** — when the customer sent it, not when we persisted it. This is what `since` filters on. |
| `client_ref` | The tier stamp. Empty when un-attributed. |
| `tenant_ref` | Tenant/outlet stamp, same rules as `client_ref`. |

An empty result is `[]`, never `null`.

---

## The tier stamp contract (read this one)

> **You MUST ignore any row whose `client_ref` you cannot resolve in your own
> database.**

One gateway serves more than one product and more than one environment. A reply
arriving here has no idea which of them it is about, and guessing would let a
production customer's "sudah" close a staging order, or the reverse.

So at ingest the gateway copies the attribution from the **most recent outbound
message this gateway sent to that same phone number**, within a 24-hour window
anchored on the reply's own timestamp. That outbound message is the question this
reply is answering, and its `client_ref` is the answer to "whose is this?".

Because those refs are minted in each product's own database, they are globally
distinct — one product's ref simply does not exist in another's. Resolve the ref
locally; act only if it is yours.

**An empty `client_ref` is the safe outcome, not a bug.** It means the gateway
sent that number nothing in the window, so nothing claims the message — an
unsolicited message, a customer writing out of the blue, a reply to a question
asked days ago. Skip it. Never guess.

The 24-hour window is chosen against the question being asked (a 4-hour
confirmation deadline is comfortably inside it) and against a customer messaging
months later being silently attributed to an ancient order.

---

## Oldest first is behavioural, not cosmetic

Rows come back in ascending `received_at`, ties broken by `id`.

If you resolve a reply against the open questions it answers, replaying out of
order lets a later message close a question an earlier one already answered.
Process the array in the order you receive it.

---

## A polling recipe

The endpoint is a read, not a push. That is deliberate: making this gateway push
would turn it into a fan-out router holding a URL, a secret, a retry policy and a
dead-letter path per subscriber — a queue it explicitly is not. A read endpoint
adds no state, and each consumer decides for itself what to act on.

1. Keep a high-water mark of the newest `received_at` you have processed.
2. Poll with `since = high_water_mark - overlap`, where `overlap` is a minute or
   two. The overlap costs you nothing and covers clock skew and late arrivals.
3. **Dedupe on `id`.** The overlap means you will see the same message twice;
   `id` is stable across redelivery, so an upsert keyed on it is a no-op the
   second time.
4. Advance the high-water mark only after the batch is processed.
5. Treat any non-200 as "try again next tick". Do not retry inside a run.

A two-minute cadence with `limit=500` is roughly several hours of headroom per
page — the cap exists to bound one runaway query, not to paginate.

```sh
BASE=http://10.10.0.4:18801
SINCE=$(date -u -d '-10 minutes' +%Y-%m-%dT%H:%M:%SZ)

curl -sS -u "$LOGIN_ID:$SECRET" \
  "$BASE/api/v1/inbound?since=$SINCE&limit=500"
```

With a bearer token instead:

```sh
TOKEN=$(curl -sS -X POST "$BASE/api/v1/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"login_id":"replace-me","secret":"replace-with-secret"}' | jq -r .token)

curl -sS "$BASE/api/v1/inbound?since=$SINCE&limit=500" \
  -H "Authorization: Bearer $TOKEN"
```

---

## What you will NOT receive

Four kinds are dropped before the database, and each would be a bug downstream
rather than mere noise:

- **Our own outbound messages.** Storing them would let a matcher read a question
  as its own answer. This also covers messages the paired account sends from
  another device.
- **Group messages.** Every question this gateway asks is asked of one person, so
  a group reply has no single sender to attribute and no question to close.
- **Status broadcasts.** Not addressed to us at all.
- **Messages with no text** — a sticker, an image with no caption, a reaction.
  Nothing to parse.

So `body` is always non-empty, and every row is a one-to-one message from a real
person to one of this gateway's numbers.

---

## When `from_phone` is empty

WhatsApp increasingly addresses senders by **LID** (a linked-identity id) rather
than by phone number. The gateway resolves a LID back to the real number before
storing, but resolution can fail — a contact the paired device has never learned
a mapping for.

Such a row is still stored, with `from_phone` empty and therefore **no stamp**.
By the contract above you will skip it. It is recorded rather than dropped so the
gateway's operator can see it happened.

Do not try to match on an empty `from_phone`.

---

## Status codes

| Code | Meaning | What to do |
|---|---|---|
| `200` | Success. Body is an array, possibly empty. | Process it. |
| `400` | `since` missing or not RFC3339. | Fix the caller. Never retried. |
| `401` | Bad or missing credentials, inactive client, expired/revoked token. | Fix the credential. |
| `503` | `{"error":"inbound log unavailable"}` — this gateway has no inbound support configured. | Distinct from a genuine failure. Degrade honestly and retry next tick. |

`503` is deliberately not `404` and not `500`: it lets you tell "this deployment
does not do inbound" from "something broke".

---

## Reaching the gateway

`/api/v1` is mounted at the service **root**, not under the browser UI's base
path, and it is **not publicly routed at all** — see the header comment in
`internal/handler/routes_api.go`. Callers reach it over container DNS or a
private address (production: `http://10.10.0.4:18801`), or through an SSH
forward. There is no public URL to point at.

Basic sends the secret on every request, so use TLS outside a trusted network.

Credentials are per-consumer API clients created on `/admin/api-clients`; the
secret is shown once. Full details, including token expiry and rotation, are in
[Auth and API clients](auth-and-api-clients.md).

---

## Latency

An inbound message is normally readable within a few seconds of arriving. The
gateway keeps its own durable copy rather than proxying a query, so a reply stays
readable after the fact regardless of what the upstream WhatsApp gateway is
doing — the `since` window is yours to choose, not a live tail you can miss.
