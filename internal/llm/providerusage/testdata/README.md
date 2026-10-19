# providerusage fixtures

Every file here is SYNTHETIC. No stored or personal credential was used to call
any endpoint; the implementer had no credential of their own. Each was written
by hand to the shape described by the public source named below, with obviously
fake ids and values. They pin the parsers' behaviour, not the live wire format.

Replace a file with a redacted real capture by running
`reliant-dev provider-usage probe --provider claude|codex --save` with your OWN
credential (see tools/reliant-dev/provider_usage_probe.go); the tests read these
files by name and keep passing if the shape is the same.

| File | Modelled on |
|---|---|
| claude_usage.json | `GET /api/oauth/usage`: anthropics/claude-code#91623 (five_hour, seven_day, limits[] weekly_scoped), robinebers/openusage `ClaudeUsageMapper.swift` |
| claude_usage_legacy_opus.json | same endpoint, legacy top-level `seven_day_opus` / `seven_day_sonnet` keys (null rows included), `Documentation: rate-limits-usage.md` §B1 |
| claude_profile.json | `GET /api/oauth/profile`: robinebers/openusage#394, `ClaudeUsageClient.swift` `ClaudeAccountProfile` |
| codex_usage.json | `GET /backend-api/wham/usage`: codex-rs `codex-backend-openapi-models` `RateLimitStatusPayload` + `backend-client` `RateLimitStatusWithResetCredits` |
| codex_usage_wrapped.json | same, nested under `rate_limits` (the `RateLimitStatusWithResetCredits` flatten shape) |
| codex_usage_limited.json | same, with `limit_reached: true` and `rate_limit_reached_type` |
