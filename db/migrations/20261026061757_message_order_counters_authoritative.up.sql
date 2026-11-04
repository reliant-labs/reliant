-- forge:backward-compatible — widens message_order_counters_kind_check with 'context_window' and only raises counters (GREATEST); the previous release's healing allocators read the same rows and keep working.
--
-- message_order_counters becomes the only source of a message's ordinal and
-- seq and of a context window's sequence: nothing numbers a row by reading
-- the highest number already written (internal/store/
-- message_order_counter_repo_ext.go). So every counter is raised here to the
-- highest number its scope already holds, once, and from then on every write
-- keeps it there. This is the last read of those maxima.

ALTER TABLE message_order_counters
    DROP CONSTRAINT message_order_counters_kind_check,
    ADD CONSTRAINT message_order_counters_kind_check
        CHECK (counter_kind = ANY (ARRAY['ordinal'::text, 'seq'::text, 'context_window'::text]));

-- A thread's ordinal.
INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
SELECT 'ordinal', thread_id, MAX(ordinal)
FROM messages
GROUP BY thread_id
ON CONFLICT (counter_kind, scope_id) DO UPDATE
SET last_assigned = GREATEST(message_order_counters.last_assigned, EXCLUDED.last_assigned);

-- A chat's seq covers every message it shows: its own, and those a branch
-- inherits through its windows' fork chain (they keep the parent's chat_id).
-- From here a fork seeds the branch's counter from its parent's.
WITH RECURSIVE chain AS (
    SELECT t.chat_id, cw.id AS window_id, cw.parent_context_window_id AS parent_id
    FROM context_windows cw
    JOIN threads t ON t.id = cw.thread_id
    UNION
    SELECT chain.chat_id, parent.id, parent.parent_context_window_id
    FROM chain
    JOIN context_windows parent ON parent.id = chain.parent_id
),
shown AS (
    SELECT chat_id, MAX(seq) AS high
    FROM messages
    GROUP BY chat_id
    UNION ALL
    SELECT chain.chat_id, MAX(m.seq)
    FROM chain
    JOIN messages m ON m.context_window_id = chain.window_id
    GROUP BY chain.chat_id
)
INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
SELECT 'seq', chat_id, MAX(high)
FROM shown
GROUP BY chat_id
ON CONFLICT (counter_kind, scope_id) DO UPDATE
SET last_assigned = GREATEST(message_order_counters.last_assigned, EXCLUDED.last_assigned);

-- A thread's context window sequence.
INSERT INTO message_order_counters (counter_kind, scope_id, last_assigned)
SELECT 'context_window', thread_id, MAX(sequence)
FROM context_windows
GROUP BY thread_id
ON CONFLICT (counter_kind, scope_id) DO UPDATE
SET last_assigned = GREATEST(message_order_counters.last_assigned, EXCLUDED.last_assigned);
