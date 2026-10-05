-- +goose Up

-- A polled source can lose its place: Gmail answers history.list with 404
-- once a stored history id is older than it keeps (about a week, sometimes
-- hours). The poller then re-baselines from "now" and fires nothing for the
-- missed window, which is right — a trigger must never replay a week of mail
-- — but silent loss is not. The last such gap is recorded here so trigger
-- health can say "mail between X and Y did not fire".
--
-- These are about the PAST and sit beside `status`, which is the source's
-- state NOW: a source that lost its place and then resumed is active, with a
-- recent gap.
--
-- status_since is when the source entered its current status. It changes
-- only on a status CHANGE (every poll rewrites last_polled_at), so a source
-- stuck in needs_reauth is one episode with a stable start — what the inbox
-- keys its "reconnect" item on, so dismissing it holds until the next one.
ALTER TABLE trigger_registrations
    ADD COLUMN last_gap_at timestamptz,
    ADD COLUMN last_gap_detail text NOT NULL DEFAULT '',
    ADD COLUMN status_since timestamptz NOT NULL DEFAULT now();
