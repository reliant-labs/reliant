-- forge:backward-compatible — a new table no previous release reads or writes.

-- Migration: org_updates
-- Created: 2026-11-12T18:03:13Z
--
-- The org feed (control-plane docs/design/org-sharing.md §5.2, D6): one row
-- per COARSE event about a chat or project shared with an org — it was
-- shared, it was made private, it changed (title, activity, state), it was
-- deleted — so every live member's "Shared with the org" list follows it
-- without a per-viewer fan-out of user_updates (O(events), not
-- O(events x members)). Detail stays on StreamChatUpdates, opened only when a
-- viewer opens the chat. A row carries ids, title, owner and the coarse
-- activity state, never anything of the transcript; a deletion or an unshare
-- is a bare marker (ids only).
--
-- sequence_number is per org, allocated from update_stream_counters
-- (stream_kind 'org', 20261112180335_update_stream_counters_org_kind), so a
-- StreamOrgUpdates client resumes from its cursor exactly as a user stream
-- does. No foreign keys: org ids are control-plane's, and a row outlives the
-- resource it reports deleted.
CREATE TABLE org_updates (
    id text PRIMARY KEY,
    org_id text NOT NULL CHECK (org_id <> ''),
    sequence_number bigint NOT NULL CHECK (sequence_number >= 1),
    update_type integer NOT NULL,
    resource_kind text NOT NULL CHECK (resource_kind IN ('project', 'chat')),
    resource_id text NOT NULL CHECK (resource_id <> ''),
    owner_user_id text NOT NULL,
    project_id text,
    -- The chat's title or the project's name; NULL on a marker.
    title text,
    data text NOT NULL DEFAULT '{}',
    created_at timestamp with time zone NOT NULL DEFAULT NOW(),
    CONSTRAINT org_updates_org_sequence_key UNIQUE (org_id, sequence_number)
);
COMMENT ON TABLE org_updates IS
    'The org feed: coarse events (shared, unshared, changed, deleted) about chats and projects shared with an org, sequenced per org for StreamOrgUpdates. Never transcript content.';
COMMENT ON COLUMN org_updates.update_type IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.OrgUpdateType';
