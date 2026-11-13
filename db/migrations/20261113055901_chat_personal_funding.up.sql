-- forge:backward-compatible — adds a new table the previous release never reads or writes.

-- Migration: chat_personal_funding
-- Created: 2026-11-13T05:59:01Z
--
-- One chat's question "continue on your own account?" and its answer
-- (control-plane docs/design/org-provider-accounts.md §10). Before a chat in an
-- org moves from the org's accounts to the person's own, the run parks and
-- the chat asks once: one row per chat, whichever of its threads met the case,
-- and the answer applies to every waiting thread.
--
--   state   asked | use_personal | keep_waiting
--   reason  limited (the org's accounts are rate-limited until org_reset_at),
--           unusable (no usable org account with no reset: a key revoked,
--           rejected or paused, a grant withdrawn, the org's credit spent),
--           unserved (no org account serves model)
--
-- use_personal is a funding lease the account loader honours at the activity
-- edge: until lease_until for 'limited'; until a turn start finds the org's
-- accounts usable again for 'unusable'; while the chat stays on model for
-- 'unserved'. The row is deleted when the episode ends (a call served by an
-- org-funded account again, or the lease's end). It is written only by the
-- worker's resolution and by ProviderAccountService.ResolvePersonalFundingChoice
-- (person-only, the chat's owner); a client never writes it directly.
--
-- personal_provider and personal_account_label name the account that would
-- serve (for the question's text); they are the person's own and go with the
-- chat (ON DELETE CASCADE) and with their account (account purge).
--
-- Post-deploy verification (prod): the table exists and is empty until an org
-- account exists (0):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify chat_personal_funding" "SELECT count(*) FROM chat_personal_funding"
--
-- Rollback: none needed. The previous release never names the table; it stays
-- in place, unread, and a waiting run keeps waiting on its own reset or probe.
-- Its one index is built in this file because the table is new and empty.
CREATE TABLE chat_personal_funding (
    chat_id                text NOT NULL PRIMARY KEY REFERENCES chats(id) ON DELETE CASCADE,
    user_id                text NOT NULL,
    org_id                 text NOT NULL,
    state                  text NOT NULL,
    reason                 text NOT NULL,
    model                  text NOT NULL DEFAULT '',
    personal_provider      text NOT NULL DEFAULT '',
    personal_account_label text NOT NULL DEFAULT '',
    org_reset_at           timestamptz,
    lease_until            timestamptz,
    asked_at               timestamptz NOT NULL DEFAULT now(),
    answered_at            timestamptz,
    -- announced: the chat's transcript has said that the person's own account
    -- serves under this answer (posted once, by the first call that uses it).
    announced              boolean NOT NULL DEFAULT false,
    updated_at             timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chat_personal_funding_ids_nonempty CHECK (user_id <> '' AND org_id <> ''),
    CONSTRAINT chat_personal_funding_state_check CHECK (state IN ('asked', 'use_personal', 'keep_waiting')),
    CONSTRAINT chat_personal_funding_reason_check CHECK (reason IN ('limited', 'unusable', 'unserved'))
);
CREATE INDEX chat_personal_funding_user_asked ON chat_personal_funding (user_id) WHERE state = 'asked';
