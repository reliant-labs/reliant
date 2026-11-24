-- forge:backward-compatible — a nullable column the previous release never reads or writes, and a comment; nothing is backfilled.

-- Migration: agent_messages_author_name
--
-- A human message sent into a running thread is queued in agent_messages and
-- saved as a messages row only when the thread drains its mailbox. The
-- author's display name AT SEND TIME (messages.author_name) must ride the
-- mailbox row with author_user_id, or the drain would have to look the name
-- up later, in the worker, after a rename could already have changed it
-- (control-plane docs/design/chat-collaboration.md §6.1, §6.2). NULL when the
-- send carried no name.
ALTER TABLE agent_messages
    ADD COLUMN author_name text;

COMMENT ON COLUMN agent_messages.author_name IS
    'The display name of the human who queued this message, at send time; copied to messages.author_name on drain. Never an email or an id.';

-- The owner's own messages carry their display name too, once collaboration
-- is on, so the model's transcript can name the owner once a second person
-- writes (author_user_id stays NULL for them).
COMMENT ON COLUMN messages.author_name IS
    'The author''s display name at send time, as the model''s transcript renders it; set for a collaborator (author_user_id set) and, once collaboration is on, for the owner (author_user_id NULL). Never an email or an id.';
