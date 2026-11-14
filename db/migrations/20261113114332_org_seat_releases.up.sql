-- forge:backward-compatible — adds a new table the previous release never reads or writes.

-- Migration: org_seat_releases
-- Created: 2026-11-13T11:43:32Z
--
-- A member's sign-in an org admin released from the org's seats
-- (org-provider-accounts.md §6, "Admin releases one seat"). The binding
-- matcher attributes every live member's sign-in in a bound workspace to the
-- org (provider_accounts.attributed_org_id, source 'workspace'); a row here
-- keeps it from matching that account to that org again, so a release outlives
-- the holder's next sign-in. ReleaseOrgSeat writes it in the transaction that
-- clears the attribution.
--
-- The exception lasts until the workspace is unbound: unbinding clears the
-- org's seats in it and their releases with them, so binding it again starts
-- afresh. It goes with the account (ON DELETE CASCADE) and with the org (the
-- gone-org purge).
--
-- Post-deploy verification (prod): the table exists and is empty until an
-- admin releases a seat (0):
--   reliant forge db query prod --database reliant --confirm-env prod \
--     --reason "verify org_seat_releases" "SELECT count(*) FROM org_seat_releases"
--
-- Rollback: none needed. The previous release never names the table; it stays
-- in place, unread. The previous release has no matcher, so nothing would
-- re-attribute a released seat under it.
CREATE TABLE org_seat_releases (
    org_id              text NOT NULL,
    account_id          text NOT NULL REFERENCES provider_accounts(id) ON DELETE CASCADE,
    released_by_user_id text NOT NULL,
    released_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, account_id),
    CONSTRAINT org_seat_releases_ids_nonempty CHECK (org_id <> '' AND released_by_user_id <> '')
);
CREATE INDEX org_seat_releases_account ON org_seat_releases (account_id);
