-- +goose Up

-- Restores temporal_payload_blobs for databases that lost it to a version
-- collision.
--
-- 20260923000000_temporal_payload_blobs.sql owned version 20260923000000.
-- #293 shipped the access-tokens migration under that same version, so a
-- database that ran main between #293 and #294 recorded 20260923000000 as
-- applied having executed the ACCESS-TOKENS SQL — the payload-blobs table was
-- never created, and goose will never revisit that version. Only a NEW version
-- can put the table back, which is what this is.
--
-- Mirrors 20260923000000 exactly, guarded so it is inert on every database that
-- already has the table (fresh installs and anything outside the window).
--
-- The table is the claim-check store for large Temporal payloads
-- (internal/temporal/claimcheck). Workflow history carries only a small
-- {"key","size","zsize"} reference; the bytes live here. Rows are
-- content-addressed, so a re-put of identical content is a no-op that only
-- refreshes last_referenced_at.
--
-- Not user-keyed on purpose: accountpurge does not touch this table. Rows age
-- out via the api-server's GC horizon (see claimcheck.DefaultGCHorizon).
CREATE TABLE IF NOT EXISTS temporal_payload_blobs (
    -- "sha256:<hex>" of the proto-marshaled (uncompressed) commonpb.Payload.
    key TEXT PRIMARY KEY,
    -- zstd-compressed proto-marshaled commonpb.Payload.
    data BYTEA NOT NULL,
    -- Uncompressed size, for observability.
    size_bytes INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_referenced_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_temporal_payload_blobs_last_ref ON temporal_payload_blobs(last_referenced_at);
