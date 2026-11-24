-- forge:backward-compatible — a nullable column the previous release never reads or writes; NULL means the chat's owner, which every existing row is.

-- Migration: agent_messages_author
--
-- A human message sent into a running thread is queued here first and saved
-- as a messages row only when the thread drains its mailbox. The author must
-- ride the mailbox row or it is lost between the two
-- (docs/design/chat-collaboration.md §6.1, in control-plane). NULL means the
-- chat's owner, and for every agent-to-agent row. No foreign key, as for
-- messages.author_user_id.
ALTER TABLE agent_messages
    ADD COLUMN author_user_id text;

COMMENT ON COLUMN agent_messages.author_user_id IS
    'The human who queued this message when not the chat owner; copied to messages.author_user_id on drain. NULL means the owner.';
