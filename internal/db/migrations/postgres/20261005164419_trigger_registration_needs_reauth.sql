-- +goose Up

-- A polled trigger whose connection can no longer authenticate (its refresh
-- grant was refused: Google's testing-mode refresh tokens expire after seven
-- days) is neither active nor in error. It is waiting on its owner to
-- reconnect, and polls do nothing until they do. Recording that as its own
-- status lets trigger health say "reconnect", and keeps the poll from being
-- retried as though it were transient.
ALTER TABLE trigger_registrations DROP CONSTRAINT trigger_registrations_status_check;
ALTER TABLE trigger_registrations
    ADD CONSTRAINT trigger_registrations_status_check
    CHECK (status IN ('active', 'error', 'needs_reauth'));
