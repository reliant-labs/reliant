-- forge:backward-compatible — adds a new table the previous release never reads or writes.

-- Migration: chat_person_picks
-- Created: 2026-11-13T14:14:33Z
--
-- The root model and provider a person picked for a chat in an org, through
-- the composer, signed in as a person (control-plane
-- docs/design/org-provider-accounts.md §4.4, §10). It is the one named
-- provider that counts as consent to spend the person's own account in the
-- chat: for the root thread's calls, in this chat, in org_id, and only while
-- the chat's selection is still at selection_version, the revision the pick
-- was written with, in the same locked transaction as that write. Every later
-- write of the selection advances its version, so it voids the pick by
-- construction (a workflow's, a preset's, an agent's, a substitution's);
-- nothing ever has to clear the row. A workflow's selector, a run's or a
-- spawn's providers and a sub-agent's model are never recorded here.
--
--   model_id       the root model selector the person picked ("<model>@<driver>",
--                  or "" when the write named none)
--   chat_provider  the provider an explicit switch bound the chat to, "" when none
--   selection_version  chat_selections.version this pick is valid at
--
-- One row per chat, replaced by each person pick (ON CONFLICT). It goes with
-- the chat (ON DELETE CASCADE) and with the person's account (account purge);
-- a branch of the chat starts without one.
--
-- Post-deploy verification (prod): the table exists (a count, any value):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify chat_person_picks" "SELECT count(*) FROM chat_person_picks"
--
-- Rollback: none needed. The previous release never names the table; it stays
-- in place, unread, and every named provider asks, as it did.
CREATE TABLE chat_person_picks (
    chat_id       text NOT NULL PRIMARY KEY REFERENCES chats(id) ON DELETE CASCADE,
    user_id       text NOT NULL,
    org_id        text NOT NULL,
    model_id      text NOT NULL DEFAULT '',
    chat_provider text NOT NULL DEFAULT '',
    selection_version bigint NOT NULL,
    at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chat_person_picks_ids_nonempty CHECK (user_id <> '' AND org_id <> '')
);
CREATE INDEX chat_person_picks_user ON chat_person_picks (user_id);
