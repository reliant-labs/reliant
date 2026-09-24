-- +goose Up

-- Claim-check store for large Temporal payloads (internal/temporal/claimcheck).
-- Workflow history carries only a small {"key","size","zsize"} reference; the
-- bytes live here. Rows are content-addressed, so a re-put of identical content
-- is a no-op that only refreshes last_referenced_at.
--
-- Not user-keyed on purpose: accountpurge does not touch this table. Rows age
-- out via the api-server's GC horizon (see claimcheck.DefaultGCHorizon).
CREATE TABLE temporal_payload_blobs (
    -- "sha256:<hex>" of the proto-marshaled (uncompressed) commonpb.Payload.
    key TEXT PRIMARY KEY,
    -- zstd-compressed proto-marshaled commonpb.Payload.
    data BYTEA NOT NULL,
    -- Uncompressed size, for observability.
    size_bytes INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_referenced_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_temporal_payload_blobs_last_ref ON temporal_payload_blobs(last_referenced_at);

-- +goose Down

DROP TABLE IF EXISTS temporal_payload_blobs;
