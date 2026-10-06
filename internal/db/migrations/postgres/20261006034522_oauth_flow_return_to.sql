-- +goose Up

-- Where the provider's redirect is relayed so the client that started the flow
-- can finish it with CompleteOAuth: the web app's callback route on an origin
-- this deployment serves, or a desktop app's loopback receiver. The callback
-- can no longer finish a flow itself: the web app and the API are different
-- sites, so the browser it lands in carries no credential for the API, and the
-- user who must be bound to the flow is only known to the client.
--
-- NULL on a flow an earlier release started; the callback refuses those, and
-- they expire within ten minutes anyway.
ALTER TABLE oauth_flows ADD COLUMN IF NOT EXISTS return_to text;
