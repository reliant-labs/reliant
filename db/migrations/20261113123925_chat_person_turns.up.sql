-- forge:backward-compatible — adds a new table the previous release never reads or writes.

-- Migration: chat_person_turns
-- Created: 2026-11-13T12:39:25Z
--
-- When a person last sent a message into a chat that a stored trigger or an
-- agent launched (org-provider-accounts.md §11.1, I2: attendance is per
-- turn). From that turn on, the chat's calls are attended: the person is
-- there to be asked, so the chat rules (§10) apply instead of an unattended
-- run's org-funded-only sources. Each trigger fire is a chat of its own, so no
-- later trigger-driven turn shares this chat. SendMessage writes it; the
-- routing activity reads it. A chat a person started (chat.start) has no
-- row: it is attended from its launch.
--
-- Post-deploy verification (prod): the table exists (a count, any value):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify chat_person_turns" "SELECT count(*) FROM chat_person_turns"
--
-- Rollback: none needed. The previous release never names the table; it stays
-- in place, unread, and decides attendance from the launch alone.
CREATE TABLE chat_person_turns (
    chat_id text NOT NULL PRIMARY KEY REFERENCES chats(id) ON DELETE CASCADE,
    user_id text NOT NULL,
    at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chat_person_turns_user_nonempty CHECK (user_id <> '')
);
