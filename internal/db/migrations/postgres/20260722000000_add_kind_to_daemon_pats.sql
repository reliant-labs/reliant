-- +goose Up
-- Generalize daemon_pats into the single PAT table for every rlnt_pat_ token,
-- discriminated by kind:
--   'daemon' — daemon <-> gateway stream auth (all pre-existing rows)
--   'api'    — user API personal access tokens that authenticate regular API
--              requests through the same middleware path as JWTs
-- Kind separation is enforced at validation time: the gRPC auth interceptor
-- accepts kind='api' only, the gateway PAT validator accepts kind='daemon' only.
ALTER TABLE daemon_pats ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'daemon';

-- Email captured at mint time so api-kind PAT auth resolves the same claims
-- object JWT validation produces. Empty for daemon-kind rows.
ALTER TABLE daemon_pats ADD COLUMN IF NOT EXISTS user_email TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE daemon_pats DROP COLUMN IF EXISTS user_email;
ALTER TABLE daemon_pats DROP COLUMN IF EXISTS kind;
