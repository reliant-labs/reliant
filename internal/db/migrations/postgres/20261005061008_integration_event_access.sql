-- +goose Up

-- Access-gated routing for app-level provider events (GitHub first).
--
-- One app-level webhook (one GitHub App, one URL) delivers every event of
-- every installation. An installation is usually an ORGANIZATION, which many
-- users belong to, and members of one org do not all see the same
-- repositories. So "the event's installation is one this user can reach" is
-- not enough to route it to the user's triggers: that would hand a private
-- repository's issues to everyone in the org. An event about a repository
-- reaches a user's trigger only when the user can see THAT repository.
--
-- integration_event_access is that fact, per user: (installation, repository)
-- pairs the user's own provider credential reports as accessible — for
-- GitHub, GET /user/installations/{id}/repositories with the user's App user
-- token, which is exactly the intersection of the App's installation and the
-- user's own access. The receiver joins it before writing anything, so an
-- event never lands in the history of a user who cannot see its repository.
--
-- It is a snapshot, so it is only ever trusted while fresh (the receiver
-- requires a recent refreshed_at), refreshed when a trigger is activated and
-- periodically while one exists, and cut immediately by the provider's own
-- revocation events (repository removed from the installation, member
-- removed from the org).
CREATE TABLE IF NOT EXISTS integration_event_access (
    user_id        text        NOT NULL,
    integration_id text        NOT NULL,
    -- The provider account the access is through: the GitHub installation id.
    account_key    text        NOT NULL,
    -- The resource: the GitHub repository id (stable across renames).
    resource_key   text        NOT NULL,
    -- A readable name for the resource (owner/name), for display only.
    resource_label text        NOT NULL DEFAULT '',
    -- The user's own id AT THE PROVIDER (the GitHub user id), so a
    -- membership-removed event can revoke exactly that user's rows.
    subject_id     text        NOT NULL DEFAULT '',
    refreshed_at   timestamptz NOT NULL,
    PRIMARY KEY (user_id, integration_id, account_key, resource_key)
);

-- Routing reads every user who can see one (account, resource).
CREATE INDEX IF NOT EXISTS idx_integration_event_access_resource
    ON integration_event_access (integration_id, account_key, resource_key);
-- Revocations by provider subject.
CREATE INDEX IF NOT EXISTS idx_integration_event_access_subject
    ON integration_event_access (integration_id, subject_id) WHERE subject_id <> '';

-- integration_access_refresh is the refresh bookkeeping per (user,
-- integration): when it last succeeded or failed, and a lease so that of
-- several api-server replicas only one refreshes a given user at a time.
CREATE TABLE IF NOT EXISTS integration_access_refresh (
    user_id         text        NOT NULL,
    integration_id  text        NOT NULL,
    refreshed_at    timestamptz,
    last_error      text        NOT NULL DEFAULT '',
    last_attempt_at timestamptz,
    leased_until    timestamptz,
    PRIMARY KEY (user_id, integration_id)
);
