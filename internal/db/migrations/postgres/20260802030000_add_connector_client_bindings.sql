-- +goose Up
--
-- Which connector a given OAuth client acts through.
--
-- An OAuth access token identifies a user, not a grant. When a user has more
-- than one connector, resolving a token to "whichever connector they have"
-- would silently hand a client authority the user never gave it — and leave no
-- record that it happened. This table is the user's explicit answer to "which
-- workspace may ChatGPT use?", captured once at consent and reused after.
--
-- The pair is unique: one client acts through exactly one connector at a time.
-- Re-consenting REPLACES the row rather than adding to it, so moving a client
-- to a different workspace does not require revoking first, and a client can
-- never accumulate access to several connectors invisibly.

CREATE TABLE connector_client_bindings (
    id         text PRIMARY KEY,
    user_id    text NOT NULL,

    -- The OAuth client's identifier, as issued by the authorization server.
    -- Not a foreign key to anything: clients register with the AS, not here.
    client_id  text NOT NULL,

    -- Deleting the connector must revoke every client acting through it, which
    -- is the behavior a user expects from "revoke this connector".
    grant_id   text NOT NULL REFERENCES connector_grants(id) ON DELETE CASCADE,

    -- Display name captured at consent, so the settings UI can say which
    -- application this is without querying the authorization server.
    client_name text NOT NULL DEFAULT '',

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT connector_client_bindings_unique UNIQUE (user_id, client_id)
);

-- The redemption path: one lookup per OAuth-authenticated request.
CREATE INDEX idx_connector_client_bindings_lookup
    ON connector_client_bindings(user_id, client_id);

-- Listing which clients act through a connector, for the settings UI and for
-- showing what a revocation is about to disconnect.
CREATE INDEX idx_connector_client_bindings_grant
    ON connector_client_bindings(grant_id);

-- +goose Down
DROP TABLE IF EXISTS connector_client_bindings;
