-- forge:backward-compatible — a new table the previous release never reads or writes.

-- Migration: chat_members
-- Created: 2026-11-24T16:20:51Z
--
-- Per-person grants on a chat (control-plane docs/design/chat-collaboration.md
-- §3.1, C2): one row per person the owner, or a member at manage, named, with
-- the level they hold. The owner is never a row: ownership is chats.user_id,
-- so there is no state in which a chat has no owner or two.
--
--   view    read it, as an org-wide viewer of a shared chat does, even when
--           the chat is private
--   reply   send, answer and approve (C3 moves the sites)
--   manage  reply, plus steer, settings and the member list
--
-- A row admits its holder only while they are a live member of the chat's org,
-- asked at every decision (internal/authz), so a removed member's row grants
-- nothing. Only internal/store reads or writes it, and only internal/authz
-- decides from it. Every change is a share_events row, written with it.
-- granted_by is who set the current level. At most 25 rows per chat, enforced
-- by the writer under the chat's row lock (store.ApplyChatMemberChange).
CREATE TABLE chat_members (
    chat_id    text NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    user_id    text NOT NULL CHECK (user_id <> ''),
    role       text NOT NULL CHECK (role IN ('view', 'reply', 'manage')),
    granted_by text NOT NULL CHECK (granted_by <> ''),
    created_at timestamp with time zone NOT NULL DEFAULT NOW(),
    PRIMARY KEY (chat_id, user_id)
);
COMMENT ON TABLE chat_members IS
    'Per-person grants on a chat (view, reply, manage); the owner is chats.user_id, never a row. Decided on only in internal/authz.';

-- The chats a person holds a grant on, newest first (the inbox and the
-- shared-with-me list, C4), and the account purge.
CREATE INDEX chat_members_user ON chat_members (user_id, created_at DESC);
