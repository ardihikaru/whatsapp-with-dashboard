# Load-Balanced Sends

`POST /api/v1/messages` sends without specifying a WhatsApp session. WSM
selects one connected session using this deterministic order:

1. Lowest `queue_len`.
2. Oldest `last_selected_at`; sessions never selected before win this tie.
3. Oldest `created_at`.
4. Session id.

The selected session is marked immediately before enqueueing the message. This
keeps repeated automatic sends distributed across equal candidates in the
single WSM process.

The response includes selected session metadata:

```json
{
  "id": "message-id",
  "status": "queued",
  "session_id": "session-id",
  "session_name": "Main Gateway"
}
```

If no connected session exists, automatic routing returns `409`.

`POST /api/v1/sessions/{id}/messages` still exists for explicit routing and
returns the same message fields plus the selected session metadata.
