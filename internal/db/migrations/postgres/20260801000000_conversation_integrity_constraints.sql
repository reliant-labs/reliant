-- +goose Up

-- The conversation tables carried exactly two constraints between them: the
-- primary keys on `messages.id` and `message_content_blocks.id`. No foreign
-- keys, no unique constraints. Every invariant the product depends on --
-- "a block belongs to a message", "a thread numbers its messages 0,1,2..",
-- "an activity produces one message" -- lived in application code or nowhere,
-- and the failures are silent because nothing in the data reveals them.
--
-- Three concrete consequences this migration ends:
--
-- 1. DeleteMessage is documented as cascading to content blocks. It does not;
--    no such foreign key exists. Every Temporal activity retry that deletes a
--    prior attempt's message leaves that message's blocks behind forever,
--    unreachable (they are only ever queried by message_id) and never
--    collected. This is unbounded storage growth proportional to retry count.
--
-- 2. Ordinal uniqueness rested entirely on SERIALIZABLE isolation plus retry,
--    with no backstop. Anyone lowering the isolation level for performance --
--    a reasonable-looking change -- silently reintroduces duplicate ordinals,
--    and duplicates are not benign: ListMessages sorts by ordinal and
--    paginates with before_ordinal, so ties reorder history nondeterministically
--    and can drop or repeat rows across pages.
--
-- 3. The idempotency guard in GetMessageByActivityID is a SELECT followed by an
--    INSERT, outside any transaction. Two concurrent retries of the same
--    activity both see no row and both insert. The ON CONFLICT(id) clause on
--    CreateMessageIfNotExists cannot help: `id` is a freshly generated UUID per
--    call, so the conflict never fires.
--
-- The cleanup below is not defensive boilerplate. It repairs a specific,
-- identified defect found in real data (see the repair-message note), and the
-- constraint is what stops that defect from recurring.

-- ---------------------------------------------------------------------------
-- Cleanup 1: repair messages were written into a nonexistent thread
-- ---------------------------------------------------------------------------
-- RepairOrphanedToolCalls synthesizes a TOOL message when a workflow ends with
-- tool calls that never got results. It allocated the new message's ordinal
-- from the thread's counter but never stored thread_id on the row, so every
-- repair message landed in thread ''. Two things followed: they collided with
-- each other on (thread_id, ordinal) -- every duplicate ordinal in the
-- production dataset was a pair of these -- and they were invisible to every
-- thread-scoped query. They remained reachable only because the primary read
-- path joins through context_window_id, which is why the repair mechanism
-- appeared to work.
--
-- The context window id is built as '<chat>:<thread>:<sequence>', so the thread
-- these rows belong to is recoverable from the column that was set correctly.
-- The writer is fixed in the same change as this migration.
UPDATE messages m
SET thread_id = w.thread_id
FROM context_windows w
WHERE m.context_window_id = w.id
  AND (m.thread_id IS NULL OR m.thread_id = '');

-- Anything still unattributable cannot be placed in a thread, and a message in
-- no thread is unreachable by design. Nothing references messages by foreign
-- key yet, so this is safe to remove -- and it must happen before the
-- thread_id foreign key below can be created.
DELETE FROM message_content_blocks
WHERE message_id IN (
    SELECT id FROM messages WHERE thread_id IS NULL OR thread_id = ''
);
DELETE FROM messages WHERE thread_id IS NULL OR thread_id = '';

-- ---------------------------------------------------------------------------
-- Cleanup 2: blocks whose message is already gone
-- ---------------------------------------------------------------------------
-- The orphans left behind by the missing cascade described above. They are
-- unreachable: ListContentBlocks and ListContentBlocksForMessages both filter
-- by message_id, and no message carries these ids.
DELETE FROM message_content_blocks b
WHERE NOT EXISTS (SELECT 1 FROM messages m WHERE m.id = b.message_id);

-- ---------------------------------------------------------------------------
-- Cleanup 3: rows whose parents are missing
-- ---------------------------------------------------------------------------
-- Empty in the datasets checked, but the foreign keys cannot be added while a
-- single violation exists, and these tables have never had referential
-- integrity enforced at any point in their history.
DELETE FROM messages m WHERE NOT EXISTS (SELECT 1 FROM chats c WHERE c.id = m.chat_id);
DELETE FROM messages m WHERE NOT EXISTS (SELECT 1 FROM threads t WHERE t.id = m.thread_id);
DELETE FROM context_windows w WHERE NOT EXISTS (SELECT 1 FROM threads t WHERE t.id = w.thread_id);
DELETE FROM threads t WHERE NOT EXISTS (SELECT 1 FROM chats c WHERE c.id = t.conversation_id);

-- ---------------------------------------------------------------------------
-- Cleanup 4: duplicate ordinals and update sequence numbers
-- ---------------------------------------------------------------------------
-- After cleanup 1 the production dataset has none of these; the remaining risk
-- is historical rows in installs we have not inspected. Renumbering would
-- rewrite conversation order, so instead the newer row of a colliding pair is
-- pushed past the thread's current maximum: order within the thread is
-- preserved for everything else, and the collision is resolved deterministically
-- by created_at.
-- The offset must be ranked across the whole THREAD, not within each colliding
-- group: two separate collisions in one thread would otherwise both be assigned
-- max+2 and simply collide with each other at the new position.
WITH losers AS (
    SELECT id, thread_id
    FROM (
        SELECT id,
               thread_id,
               row_number() OVER (PARTITION BY thread_id, ordinal ORDER BY created_at, id) AS rn
        FROM messages
    ) ranked
    WHERE rn > 1
),
relocations AS (
    SELECT l.id,
           l.thread_id,
           row_number() OVER (PARTITION BY l.thread_id ORDER BY l.id) AS offset_in_thread
    FROM losers l
),
maxima AS (
    SELECT thread_id, max(ordinal) AS max_ordinal FROM messages GROUP BY thread_id
)
UPDATE messages m
SET ordinal = mx.max_ordinal + r.offset_in_thread
FROM relocations r
JOIN maxima mx ON mx.thread_id = r.thread_id
WHERE m.id = r.id;

-- chat_updates.sequence_number is the cursor the streaming client resumes from.
-- Its sibling table user_updates has carried UNIQUE(user_id, sequence_number)
-- since it was created; chat_updates was left without the equivalent, resting
-- on the same isolation-level argument as ordinals. Duplicates here mean a
-- reconnecting client can skip or replay updates.
-- Ranked per chat for the same reason as the ordinal relocation above.
WITH losers AS (
    SELECT id, chat_id
    FROM (
        SELECT id,
               chat_id,
               row_number() OVER (PARTITION BY chat_id, sequence_number ORDER BY created_at, id) AS rn
        FROM chat_updates
    ) ranked
    WHERE rn > 1
),
relocations AS (
    SELECT l.id,
           l.chat_id,
           row_number() OVER (PARTITION BY l.chat_id ORDER BY l.id) AS offset_in_chat
    FROM losers l
),
maxima AS (
    SELECT chat_id, max(sequence_number) AS max_seq FROM chat_updates GROUP BY chat_id
)
UPDATE chat_updates u
SET sequence_number = mx.max_seq + r.offset_in_chat
FROM relocations r
JOIN maxima mx ON mx.chat_id = r.chat_id
WHERE u.id = r.id;

-- Duplicate (chat_id, activity_id) means an activity retry produced a second
-- message instead of being deduped. The newest row is the one the retry
-- produced and the one no reader reached, so it is the one to drop.
DELETE FROM message_content_blocks
WHERE message_id IN (
    SELECT id FROM (
        SELECT id, row_number() OVER (
            PARTITION BY chat_id, activity_id ORDER BY created_at, id
        ) AS rn
        FROM messages WHERE activity_id IS NOT NULL AND activity_id <> ''
    ) x WHERE rn > 1
);
DELETE FROM messages WHERE id IN (
    SELECT id FROM (
        SELECT id, row_number() OVER (
            PARTITION BY chat_id, activity_id ORDER BY created_at, id
        ) AS rn
        FROM messages WHERE activity_id IS NOT NULL AND activity_id <> ''
    ) x WHERE rn > 1
);

-- ---------------------------------------------------------------------------
-- Constraints
-- ---------------------------------------------------------------------------
-- ON DELETE choices are deliberate. A message without its chat, and a block
-- without its message, are meaningless and cascade. A message without its
-- thread is equally meaningless, but threads are never deleted in this codebase
-- and a cascade there would make a future thread cleanup silently destroy
-- history, so it RESTRICTs instead -- the safer failure is a loud one.
ALTER TABLE messages
    ADD CONSTRAINT messages_chat_id_fkey
    FOREIGN KEY (chat_id) REFERENCES chats(id) ON DELETE CASCADE;

ALTER TABLE messages
    ADD CONSTRAINT messages_thread_id_fkey
    FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE RESTRICT;

ALTER TABLE message_content_blocks
    ADD CONSTRAINT message_content_blocks_message_id_fkey
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE CASCADE;

ALTER TABLE threads
    ADD CONSTRAINT threads_conversation_id_fkey
    FOREIGN KEY (conversation_id) REFERENCES chats(id) ON DELETE CASCADE;

ALTER TABLE context_windows
    ADD CONSTRAINT context_windows_thread_id_fkey
    FOREIGN KEY (thread_id) REFERENCES threads(id) ON DELETE CASCADE;

-- Makes an ordinal collision fail loudly at write time instead of silently
-- reordering a conversation at read time. This also replaces the SERIALIZABLE
-- isolation level as the *only* thing standing between concurrent writers and
-- duplicate ordinals -- the isolation level still prevents the collision; this
-- catches it if that ever stops being true.
ALTER TABLE messages
    ADD CONSTRAINT messages_thread_ordinal_key UNIQUE (thread_id, ordinal);

-- Closes the check-then-insert race in the activity idempotency guard. Partial
-- because activity_id is legitimately null for messages not produced by a
-- workflow activity (user messages, injected system messages).
CREATE UNIQUE INDEX messages_chat_activity_key
    ON messages (chat_id, activity_id)
    WHERE activity_id IS NOT NULL AND activity_id <> '';

-- The constraint user_updates has had all along.
ALTER TABLE chat_updates
    ADD CONSTRAINT chat_updates_chat_sequence_key UNIQUE (chat_id, sequence_number);

-- +goose Down

ALTER TABLE chat_updates DROP CONSTRAINT IF EXISTS chat_updates_chat_sequence_key;
DROP INDEX IF EXISTS messages_chat_activity_key;
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_thread_ordinal_key;
ALTER TABLE context_windows DROP CONSTRAINT IF EXISTS context_windows_thread_id_fkey;
ALTER TABLE threads DROP CONSTRAINT IF EXISTS threads_conversation_id_fkey;
ALTER TABLE message_content_blocks DROP CONSTRAINT IF EXISTS message_content_blocks_message_id_fkey;
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_thread_id_fkey;
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_chat_id_fkey;

-- The cleanup deletions are not reversible; the rows they removed were
-- unreachable by every query path in the application.
