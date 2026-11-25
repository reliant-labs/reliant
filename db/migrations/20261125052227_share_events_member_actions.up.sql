-- forge:backward-compatible — adds two nullable columns and widens share_events' action CHECK by three values; every row the previous release writes (no target, share/unshare/admin_unshare/moved) still satisfies every constraint, and it never reads the new columns or values.

-- Migration: share_events_member_actions
-- Created: 2026-11-25T05:22:26Z
--
-- A change of a chat's per-person grants (chat_members; control-plane
-- docs/design/chat-collaboration.md §3.1) is recorded in share_events beside
-- the visibility changes, so who granted what to whom is one append-only
-- table:
--
--   grant        actor_user_id gave target_user_id a level (role)
--   role_change  actor_user_id changed target_user_id's level to role
--   revoke       actor_user_id removed target_user_id's grant (role NULL)
--
-- target_user_id is set on those three, and on a 'moved' row that records a
-- grant a project's move to another org cleared (store.MoveProjectOrg); never
-- on a visibility change. role is the level a grant or role_change leaves.
--
-- One ALTER TABLE: one lock, and no instant without the constraints. The
-- columns are nullable with no default (a catalog change), and every CHECK is
-- added NOT VALID, so the ACCESS EXCLUSIVE lock covers only the catalog, and
-- lock_timeout bounds the wait for it.
-- 20261125052228_share_events_member_actions_validate validates the existing
-- rows in its own transaction under SHARE UPDATE EXCLUSIVE. Every existing row
-- has no target and an action the narrower CHECK admitted, so validation
-- cannot fail.
SET LOCAL lock_timeout = '5s';
ALTER TABLE share_events
    ADD COLUMN target_user_id text,
    ADD COLUMN role text,
    DROP CONSTRAINT share_events_action_check,
    ADD CONSTRAINT share_events_action_check
        CHECK (action IN ('share', 'unshare', 'admin_unshare', 'moved', 'grant', 'revoke', 'role_change')) NOT VALID,
    ADD CONSTRAINT share_events_target_check
        CHECK ((target_user_id IS NULL OR target_user_id <> '')
               AND (action NOT IN ('grant', 'revoke', 'role_change') OR target_user_id IS NOT NULL)
               AND (action IN ('grant', 'revoke', 'role_change', 'moved') OR target_user_id IS NULL)) NOT VALID,
    ADD CONSTRAINT share_events_role_check
        CHECK (role IS NULL OR (role IN ('view', 'reply', 'manage') AND action IN ('grant', 'role_change'))) NOT VALID;
