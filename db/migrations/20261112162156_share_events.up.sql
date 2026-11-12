-- forge:backward-compatible — a new table no previous release reads or writes.

-- Migration: share_events
-- Created: 2026-11-12T16:21:56Z
--
-- The audit trail of read-only sharing (control-plane
-- docs/design/org-sharing.md §9): one row per change of a project's, chat's or
-- saved workflow's visibility, written in the same transaction as the change.
--
--   share          the owner shared it with the org
--   unshare        the owner made it private again
--   admin_unshare  an org admin cleared a member's share (moderation)
--
-- org_id is the resource's org (the share's audience); owner_user_id whose
-- resource it is; actor_user_id who changed it (the owner, or the admin). No
-- foreign keys: the resource may be deleted later and its history kept, and
-- org and user ids are control-plane's.
CREATE TABLE share_events (
    id text PRIMARY KEY,
    org_id text NOT NULL CHECK (org_id <> ''),
    resource_kind text NOT NULL CHECK (resource_kind IN ('project', 'chat', 'workflow')),
    resource_id text NOT NULL CHECK (resource_id <> ''),
    owner_user_id text NOT NULL CHECK (owner_user_id <> ''),
    actor_user_id text NOT NULL CHECK (actor_user_id <> ''),
    action text NOT NULL CHECK (action IN ('share', 'unshare', 'admin_unshare')),
    created_at timestamp with time zone NOT NULL DEFAULT NOW()
);
COMMENT ON TABLE share_events IS
    'One row per change of a resource''s org visibility (share, unshare, admin_unshare), written with the change. Append-only.';

-- An org admin reads their org's sharing history newest first; a resource's
-- history is read by kind and id.
CREATE INDEX share_events_org_created ON share_events (org_id, created_at DESC);
CREATE INDEX share_events_resource ON share_events (resource_kind, resource_id, created_at DESC);
