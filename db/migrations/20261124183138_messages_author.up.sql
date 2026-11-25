-- forge:backward-compatible — two nullable columns the previous release never reads or writes; NULL means the chat's owner, which is what every existing row is, so nothing is backfilled.

-- Migration: messages_author
--
-- The human who wrote a message, when that is not the chat's owner
-- (docs/design/chat-collaboration.md §6.1, in control-plane). Both NULL for
-- the owner's own messages and for every model, tool or agent message: NULL
-- means "the owner", so every row written before this column is already
-- correct.
--
-- author_user_id is distinct from sender_thread_id / sender_kind, which name
-- the AGENT that sent a message (20261026165300_message_sender). It has no
-- foreign key: a message is history and must outlive its author's account.
--
-- author_name is the author's display name AT SEND TIME. It is what the
-- model's transcript names the author with, so a rename or a departure never
-- changes prompt bytes already sent (prompt cache), and a member who has left
-- still has a name. A display name only, never an email or an id.
ALTER TABLE messages
    ADD COLUMN author_user_id text,
    ADD COLUMN author_name text;

COMMENT ON COLUMN messages.author_user_id IS
    'The human author when not the chat owner (chat-collaboration.md §6.1); NULL means the owner. No foreign key: history outlives accounts.';
COMMENT ON COLUMN messages.author_name IS
    'The author''s display name at send time, as the model''s transcript renders it; NULL for the owner. Never an email or an id.';
