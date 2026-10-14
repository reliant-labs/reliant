-- +goose Up

-- Who an automation's integration writes speak as. `owner` is the activation's
-- owner, through their GitHub App user token (what every activation did before
-- this column). `app` is the GitHub App's bot (`reliant-labs[bot]`), through a
-- per-repository installation token that control-plane mints only when the
-- owner can push to that repository.
--
-- NOT NULL with a default, so every existing row keeps acting as its owner and
-- the previous release keeps working against the new schema. Self-hosted
-- deployments, which have no control plane to mint from, only ever hold
-- `owner` (enforced when a trigger is written).
--
-- Re-runnable (see 20261005170614).
ALTER TABLE triggers ADD COLUMN IF NOT EXISTS acting_identity text NOT NULL DEFAULT 'owner';
ALTER TABLE triggers DROP CONSTRAINT IF EXISTS triggers_acting_identity_check;
ALTER TABLE triggers ADD CONSTRAINT triggers_acting_identity_check CHECK (acting_identity IN ('owner', 'app'));
