-- forge:backward-compatible — a new index; no release depends on it, and the previous release's writes simply maintain it.

-- Migration: message_content_blocks_attachment_index
--
-- An attachment a chat member attaches to a message in someone else's chat
-- is readable by that chat's owner as well as its uploader (control-plane
-- docs/design/chat-collaboration.md §6.4). The read asks "is this attachment
-- in a message of a chat this person owns" (store.AttachedInChatOwnedBy),
-- which starts from the attachment id a content block holds: image (4), file
-- reference (6) and document (7) blocks carry the attachment's id as their
-- content. Without this index that is a scan of every content block.
--
-- CONCURRENTLY cannot run in a transaction block, so this is the only
-- statement in the file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_blocks_attachment
    ON public.message_content_blocks (content)
    WHERE block_type IN (4, 6, 7);
