-- +goose Up
--
-- A queued message from the user (SendAgentMessage) can carry attachments,
-- exactly like an ordinary SendMessage turn -- the composer offers no way to
-- know a message is being queued rather than sent until it is too late to
-- ask the user to drop their screenshot. attachments stores the attachment
-- IDs as a JSON array, mirroring connector_grants.allowed_tools
-- (20260802010000_add_connector_grants.sql): there is no existing text[]
-- convention in this schema, and this column is read/written wholesale
-- (never queried by individual element), so jsonb needs no GIN index either.
ALTER TABLE agent_messages ADD COLUMN attachments jsonb;

-- +goose Down

ALTER TABLE agent_messages DROP COLUMN attachments;
