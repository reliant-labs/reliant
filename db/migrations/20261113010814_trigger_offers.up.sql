-- forge:backward-compatible — adds a table only this release reads and writes; the previous release never reads it, so after a rollback no offer can be accepted (the rpcs are gone) and the rows are inert. No existing table or column changes.

-- Migration: trigger_offers
--
-- Admin takeover of a departed member's automations (control-plane
-- docs/design/org-sharing.md §8). Every run of a trigger executes as its
-- owner, so nobody becomes the owner of one without accepting it: an org
-- admin adopts an orphaned trigger to themselves directly, or OFFERS it to
-- another member, and only that member's AcceptTriggerOffer re-owns it.
--
-- A row is one offer. It is live only while it is pending, unexpired, its
-- offerer and offeree are both still live members of the org, and the
-- trigger is still owned by previous_owner_user_id (its departed creator):
-- the server checks all of that when the offer is read or accepted, and
-- settles a dead one as 'expired' or 'revoked' then. Membership lives in
-- control-plane, so a departure cannot cascade here.
CREATE TABLE trigger_offers (
    id text PRIMARY KEY,
    trigger_id text NOT NULL REFERENCES triggers (id) ON DELETE CASCADE,
    org_id text NOT NULL CHECK (org_id <> ''),
    -- The trigger's owner when the offer was made: an accept re-owns the
    -- trigger only while it still is, so an offer cannot outlive a rejoin or
    -- another adoption.
    previous_owner_user_id text NOT NULL,
    offered_by_user_id text NOT NULL,
    offeree_user_id text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
    -- Why a revoked offer was revoked: superseded, declined, offerer_left,
    -- offeree_left, owner_changed, withdrawn. Empty otherwise.
    status_reason text NOT NULL DEFAULT '',
    created_at timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at timestamp with time zone NOT NULL,
    settled_at timestamp with time zone
);

-- At most one pending offer per trigger: a new offer supersedes the last.
CREATE UNIQUE INDEX trigger_offers_one_pending ON trigger_offers (trigger_id) WHERE status = 'pending';
-- An offeree's inbox of offers.
CREATE INDEX trigger_offers_offeree ON trigger_offers (offeree_user_id, org_id) WHERE status = 'pending';

COMMENT ON TABLE trigger_offers IS 'forge:no-entity';
COMMENT ON COLUMN trigger_offers.status IS
    'pending: waiting for the offeree; accepted: the offeree adopted the trigger; revoked: superseded, declined, withdrawn, or a party left; expired: not accepted within its window.';
