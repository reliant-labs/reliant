-- forge:backward-compatible — additive: two nullable columns on provider_account_limits that the previous release never reads or writes.

-- last_traffic_at: when a response served by the account last carried rate-limit headers.
-- observed_at cannot say, because a usage-API poll writes it too; the poller needs the
-- difference to leave accounts nothing has used for a day alone (design §4.3).
-- usage_polled_at: when the usage API was last called for the account, claimed atomically so
-- the on-demand refresh and the poller share one 60-second floor across every replica.
ALTER TABLE provider_account_limits ADD COLUMN last_traffic_at timestamptz;
ALTER TABLE provider_account_limits ADD COLUMN usage_polled_at timestamptz;
