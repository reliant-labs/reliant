-- forge:backward-compatible — widens share_events' action CHECK by one value ('moved'); every row the previous release writes (share, unshare, admin_unshare) still satisfies it, and it never reads the new value.

-- Migration: share_events_action_moved
-- Created: 2026-11-13T04:13:07Z
--
-- Moving a project to another org clears every share of the project and its
-- chats (control-plane docs/design/org-project-move.md §1.2, D7) and records
-- each cleared share in share_events with action 'moved', in the source org
-- (the audience that lost it). Widen the CHECK to admit it.
--
-- DROP and ADD are one ALTER TABLE: one lock, and no instant without the
-- constraint. The new CHECK is added NOT VALID, so the ACCESS EXCLUSIVE lock
-- covers only the catalog change, and lock_timeout bounds the wait for it.
-- 20261113041313_share_events_action_moved_validate validates the existing
-- rows in its own transaction under SHARE UPDATE EXCLUSIVE. Every existing row
-- satisfied the narrower CHECK this replaces, so validation cannot fail.
SET LOCAL lock_timeout = '5s';
ALTER TABLE share_events
    DROP CONSTRAINT share_events_action_check,
    ADD CONSTRAINT share_events_action_check
        CHECK (action IN ('share', 'unshare', 'admin_unshare', 'moved')) NOT VALID;
