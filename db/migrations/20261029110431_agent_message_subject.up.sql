-- forge:backward-compatible — two added columns the previous release never reads or writes: agent_messages.subject defaults to '' for its inserts, and messages.sender_subject is nullable.

-- The one-line subject an agent gives a message it sends another agent
-- (spawn_send's required `subject`). The transcript shows an agent-to-agent
-- message collapsed to this line; the body is one click away.
--
-- agent_messages.subject is '' for every kind that has none (a human's queued
-- message, a sub-agent's completion report) and for rows written before it
-- existed. messages.sender_subject travels with sender_thread_id/sender_kind
-- when a mailbox row is drained into history, and is NULL whenever they are.
ALTER TABLE agent_messages
    ADD COLUMN subject text DEFAULT '' NOT NULL;

ALTER TABLE messages
    ADD COLUMN sender_subject text;
