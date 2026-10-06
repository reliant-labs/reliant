-- +goose Up

-- The provider's id for the PERSON who made a connection, as that provider's
-- events name them in trigger.sender.id: a Slack user id, a GitHub login, a
-- Gmail address. It is what a trigger's "Only from: Me" allowlists.
--
-- Distinct from external_account_id, which is the ACCOUNT a connection acts
-- as and routes on: a Slack connection's account is the workspace (team_id),
-- and its sender is the user who installed the app there. Slack only says who
-- that is in the OAuth exchange (authed_user.id), so a connection made before
-- this column has none until it is reconnected; NULL means "not known".
ALTER TABLE connections ADD COLUMN IF NOT EXISTS sender_id text;
