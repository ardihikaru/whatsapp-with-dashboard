-- 0012_contacts.sql — an owner-managed WhatsApp contact book: a name, a
-- canonical phone number, optional lowercase aliases, and whether that
-- number is actually registered on WhatsApp (verified through the existing
-- waxum ContactDirectory, see internal/handler/contacts_check.go's
-- checkContacts — this migration adds no new verification path of its
-- own).
--
-- GLOBAL, not per-session — unlike 0010's wa_known_groups (a session's own
-- picker source for groups IT belongs to), a contact is a person the
-- OPERATOR knows, independent of which session happens to be connected
-- when the number is checked or a message is later sent to them. phone is
-- therefore UNIQUE across the whole table, not scoped to a session_id.
--
-- phone stores CANONICAL digits (wa.NormalizePhone's output, e.g.
-- "6281200000000"), the same normalisation checkContacts already runs
-- every input through, so this table and that function can never disagree
-- about what one phone number looks like.
--
-- aliases is a single comma-separated, lowercase VARCHAR rather than a
-- child table: this is a small per-contact list of extra names/nicknames a
-- ranked search matches against (internal/contactmatch), not a relation
-- queried on its own, and a free-text column is the simplest thing that
-- can hold it. Writers normalise case; readers must not assume it is.
--
-- wa_status/verified_at/jid record the LAST verification outcome, not a
-- live state: "registered"/"not_registered" is only ever as fresh as the
-- most recent Verify action (create-time or an explicit re-verify), and a
-- routing/backend failure must leave a contact at "unknown" rather than
-- claim "not_registered" for a number nobody actually got an answer about
-- — see checkContacts' own doc comment on that same distinction.
--
-- laundry_wa only — outside the platform conformance inventory (see 0001's
-- header).

-- +goose Up
CREATE TABLE wa_contacts (
  id          VARCHAR(64)  NOT NULL,
  name        VARCHAR(120) NOT NULL,
  phone       VARCHAR(32)  NOT NULL,
  aliases     VARCHAR(500) NOT NULL DEFAULT '',
  jid         VARCHAR(64)  NOT NULL DEFAULT '',
  wa_status   VARCHAR(20)  NOT NULL DEFAULT 'unknown',
  verified_at VARCHAR(40)  NOT NULL DEFAULT '',
  created_at  VARCHAR(40)  NOT NULL,
  updated_at  VARCHAR(40)  NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY uq_wa_contacts_phone (phone)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- +goose Down
DROP TABLE IF EXISTS wa_contacts;
