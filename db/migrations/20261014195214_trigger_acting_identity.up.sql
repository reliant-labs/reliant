-- forge:backward-compatible — the column is NOT NULL with a default of 'owner', so the previous release neither reads nor writes it and every row keeps acting as its owner.

-- Who an automation's integration writes speak as. `owner` is the activation's
-- owner, through their GitHub App user token (what every activation did before
-- this column). `app` is the GitHub App's bot (`reliant-labs[bot]`), through a
-- per-repository installation token that control-plane mints only when the
-- owner can push to that repository.
--
-- Self-hosted deployments, which have no control plane to mint from, only ever
-- hold `owner` (enforced when a trigger is written).
ALTER TABLE triggers ADD COLUMN acting_identity text NOT NULL DEFAULT 'owner';
ALTER TABLE triggers ADD CONSTRAINT triggers_acting_identity_check CHECK (acting_identity IN ('owner', 'app'));
