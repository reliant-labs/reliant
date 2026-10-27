-- forge:backward-compatible — two nullable columns the previous release never reads or writes; NULL means the human, which is what every existing row is.

-- Who sent a message that did not come from the human: the sending thread and
-- the mailbox kind (core.AgentMessageKind) it arrived as. Both NULL for a
-- human-authored or model-produced message.
--
-- sender_thread_id deliberately has no foreign key. A message is history: it
-- must outlive its sender (ON DELETE SET NULL would turn an agent's message
-- into a human one, RESTRICT would make the sender's thread undeletable).
-- Readers tolerate a thread that no longer exists.
ALTER TABLE messages
    ADD COLUMN sender_thread_id text,
    ADD COLUMN sender_kind integer;

COMMENT ON COLUMN messages.sender_kind IS 'forge:go-type=github.com/reliant-labs/reliant/gen/reliant/v1;reliantv1.AgentMessageKind';
