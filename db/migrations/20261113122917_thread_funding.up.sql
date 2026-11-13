-- forge:backward-compatible — adds a new table the previous release never reads or writes.

-- Migration: thread_funding
-- Created: 2026-11-13T12:29:17Z
--
-- Who paid for a thread's calls since its account last changed: the funding
-- class (llm_call_usage.funding's values), the org of the run, and the account
-- (org-provider-accounts.md §10.1, case 2). The routing activity writes it when
-- a call places a thread on a new account and goes ahead. Before it moves a
-- thread from an org-funded account to the person's own, it reads this row, so
-- "this thread was on the org's money" is known per thread and survives the
-- org account's deletion. account_id has no foreign key for that reason.
-- The row goes with the thread (ON DELETE CASCADE).
--
-- Post-deploy verification (prod): the table exists and is empty until an org
-- account exists (0):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify thread_funding" "SELECT count(*) FROM thread_funding"
--
-- Rollback: none needed. The previous release never names the table; it stays
-- in place, unread.
CREATE TABLE thread_funding (
    thread_id  text NOT NULL PRIMARY KEY REFERENCES threads(id) ON DELETE CASCADE,
    org_id     text NOT NULL DEFAULT '',
    funding    text NOT NULL,
    account_id text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT thread_funding_funding_check CHECK (funding IN ('org_key', 'org_seat', 'org_wallet', 'personal', 'local'))
);
