# Load-Balanced Sends

`POST /api/v1/messages` sends without specifying a WhatsApp session. WSM picks
one for you. Two things decide which one: the **scope** the request asks for,
and the **ranking** inside that scope.

## 1. The scope — which sessions are even candidates

Every session carries two routing-intent fields, both independent of whether it
is connected: `is_master` (a boolean) and `group_code` (a string, `""` when
unset). A request carries `group_code`, `master_only` and `allow_master`. The
candidate set is:

| Request | Candidates |
|---|---|
| *(nothing)* | `group_code == ""` and `!is_master` |
| `allow_master: true` | `group_code == ""` |
| `master_only: true` | `group_code == ""` and `is_master` |
| `group_code: "X"` | `group_code == "X"` and `!is_master` |
| `group_code: "X"` + `allow_master: true` | `group_code == "X"` |
| `group_code: "X"` + `master_only: true` | `group_code == "X"` and `is_master` |

Two consequences are worth stating outright, because both surprise people:

- **A session with a group code is invisible to a request without one.** The
  match is exact in both directions — `""` matches only `""`, and a named group
  matches only that same name. Assigning a group code therefore *removes* a
  session from default routing; that is the point of it, and it is also the way
  the feature most commonly bites.
- **There is no fallback between scopes, ever.** A `group_code` send whose group
  is entirely offline answers 409 even when a dozen other sessions are connected.
  Quietly routing through one of them would defeat the only reason to ask for a
  scope.

`master_only` overrides `allow_master`: "only the master" already answers "may
the master be used".

## 2. The ranking — which candidate wins

Deterministic, in this order:

1. Lowest `queue_len`.
2. Oldest `last_selected_at`; sessions never selected before win this tie.
3. Oldest `created_at`.
4. Session id.

The selected session is marked immediately before enqueueing the message, which
keeps repeated automatic sends distributed across equal candidates in the single
WSM process.

## 3. The response reports the decision it made

```json
{
  "id": "message-id",
  "status": "queued",
  "session_id": "session-id",
  "session_name": "Main Gateway",
  "session_group_code": "",
  "session_is_master": false,
  "routing": "auto"
}
```

`routing` is one of `auto`, `master_only`, `group`, `group_master_only`, or
`explicit` (the `/api/v1/sessions/{id}/...` routes, which name their own session
and ignore all three routing fields).

`session_group_code` is **always present**, `""` when the chosen session is
ungrouped. An absent key would be indistinguishable from an older gateway that
never had `group_code` at all, so the empty string is used to say "ungrouped"
unambiguously.

## 4. When nothing matches

Automatic routing returns **409**, and the body names the cause rather than just
the symptom — a scoped send failing on a healthy gateway must not read as an
outage:

- ungrouped, nothing connected → `no connected session available`
- ungrouped, the only connected sessions are masters →  the same, plus a hint
  naming `allow_master`
- `master_only`, no connected master → names the offline master handset
- `group_code`, nothing carries that code → says so and suggests checking the
  spelling against the Group column on `/sessions`
- `group_code`, the group is dark but other sessions are connected → says
  explicitly that the gateway is *not* down
- `group_code` + `master_only`, the group is up but has no master → says to flag
  one, or clear `master_only`

Every group-scoped 409 ends with the same escape hatch — omit `group_code` to
route through any connected session — so an on-call reader who has never heard
of the feature can still get a message out.

## 5. Explicit routing

`POST /api/v1/sessions/{id}/messages` still exists and is unaffected by every
field above: naming a session id reaches it whatever its group code or master
flag. It returns the same message fields plus the same session metadata.
