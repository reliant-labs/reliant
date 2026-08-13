-- +goose Up
-- Replace the SELECT MAX(sequence_number)+1 allocation for user_updates and
-- chat_updates with real Postgres sequences.
--
-- Every transaction in this app runs at SERIALIZABLE (see BeginImmediate in
-- internal/db/repo.go). Under SERIALIZABLE, `SELECT MAX(sequence_number)
-- WHERE user_id = ?` is a PREDICATE read: it takes a lock covering every row
-- that would satisfy it, including rows that do not exist yet. Any concurrent
-- INSERT for that same user falls inside the predicate, so the two transactions
-- form a read/write dependency and Postgres aborts one with
--
--   ERROR: could not serialize access due to read/write dependencies among
--   transactions (SQLSTATE 40001)
--
-- This is not a rare race. The allocation is on the write path of essentially
-- every user-visible event — activity changes, workflow status, message saves,
-- mailbox drains — and all of a user's chats share ONE counter. With several
-- spawns running concurrently the conflict rate scales with fan-out, and the
-- observed failure burned all 3 retries in 3.0s and surfaced to the user as a
-- failed SendMessage ("failed to get max user sequence ... 40001"), which in
-- turn left a paused workflow unresumed.
--
-- A sequence removes the conflict at the source rather than retrying harder:
-- nextval() is non-transactional, takes no predicate lock, and never blocks a
-- concurrent caller. Two transactions allocating at once get two distinct
-- values and neither aborts.
--
-- The cost is that sequence values are NOT gap-free: a rolled-back or retried
-- transaction consumes a value permanently. That is safe here because nothing
-- requires contiguity — every consumer treats these as an ordered cursor, not a
-- dense range. The readers are `WHERE sequence_number > $cursor ORDER BY
-- sequence_number ASC` (GetUserUpdatesSince / GetUpdatesSince) and the
-- activityStore's monotonic watermark comparisons, all of which only need
-- values to increase. Gaps also already occur today, since a failed insert
-- after a successful MAX() read leaves its number unused.
--
-- Both UNIQUE(user_id, sequence_number) and UNIQUE(chat_id, sequence_number)
-- are kept. A shared sequence per table means values are globally unique rather
-- than per-user/per-chat, which trivially satisfies both.

CREATE SEQUENCE IF NOT EXISTS user_updates_sequence_number_seq AS bigint;
CREATE SEQUENCE IF NOT EXISTS chat_updates_sequence_number_seq AS bigint;

-- Start each sequence above every value already in the table so existing
-- clients' cursors stay valid and no historical row can be duplicated.
-- setval's third argument (is_called = true) makes the NEXT nextval return
-- start+1 rather than start.
--
-- The headroom is not decoration. During a rolling deploy the OLD binary is
-- still allocating with MAX()+1 against these same tables, so between this
-- setval and the last old process exiting, the table max keeps climbing past
-- whatever we set. A sequence seeded at exactly max would then hand out values
-- that already exist and every insert would fail the UNIQUE constraint until
-- the sequence caught up. Seeding well clear of the live tip means the two
-- allocators cannot collide while both are briefly running, at the only cost
-- of a one-time gap — which is free, since nothing requires contiguity.
SELECT setval(
    'user_updates_sequence_number_seq',
    (SELECT COALESCE(MAX(sequence_number), 0) + 100000 FROM user_updates),
    true
);
SELECT setval(
    'chat_updates_sequence_number_seq',
    (SELECT COALESCE(MAX(sequence_number), 0) + 100000 FROM chat_updates),
    true
);

-- +goose Down
DROP SEQUENCE IF EXISTS user_updates_sequence_number_seq;
DROP SEQUENCE IF EXISTS chat_updates_sequence_number_seq;
