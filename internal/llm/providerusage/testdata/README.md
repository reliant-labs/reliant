# providerusage fixtures

The three endpoint fixtures are LIVE captures, redacted, taken 2026-10-19 from
the repo owner's own accounts (a Claude Max 20x subscription and a ChatGPT
pro-lite subscription) with their stored OAuth access tokens. Every call was a
single read-only GET; no token was refreshed. Emails, uuids, ids, names and free
text are replaced by placeholders (`user<N>@example.invalid`, all-zero uuids,
`redacted-<N>`); structure, field names, numbers, enums and timestamps are as
the provider sent them. Re-capture with `reliant-dev provider-usage probe
--provider claude|codex --save <dir>` using your OWN credential.

| File | Source | Live finding |
|---|---|---|
| claude_usage.json | `GET api.anthropic.com/api/oauth/usage` (HTTP 200) | `five_hour` / `seven_day` carry `utilization` 0..100 and `resets_at`; `limits[]` rows have `kind` session / weekly_all / weekly_scoped (scoped rows carry `scope.model.display_name`) and `percent`; `seven_day_opus` / `seven_day_sonnet` are null; the body also has `seven_day_breakdown`, `spend`, `extra_usage`, `weekly_scoped_shares` and a dozen codename keys (all null) that the parser ignores |
| claude_profile.json | `GET api.anthropic.com/api/oauth/profile` (HTTP 200) | `account.{uuid,email,has_claude_max,has_claude_pro}`, `organization.{uuid,name,organization_type:"claude_max",rate_limit_tier:"default_claude_max_20x",subscription_status}`; there is no plan string, so the plan is derived from `organization_type` + `rate_limit_tier` |
| codex_usage.json | `GET chatgpt.com/backend-api/wham/usage` (HTTP 200) | top-level (not wrapped) `plan_type:"prolite"`, `rate_limit.{allowed,limit_reached,primary_window,secondary_window}`; this plan has ONE weekly window in `primary_window` (`limit_window_seconds` 604800) and `secondary_window: null`; while 100% used it still says `allowed:true, limit_reached:false`, so status comes from the window math |

The remaining files are SYNTHETIC, written by hand to cover shapes the live
accounts did not produce. They pin parser behaviour for those shapes only:

| File | Models |
|---|---|
| claude_usage_synthetic_scoped.json | several `weekly_scoped` limits[] rows (a spent one, a decimal one, malformed ones) and null legacy keys |
| claude_usage_legacy_opus.json | legacy top-level `seven_day_opus` / `seven_day_sonnet` keys |
| codex_usage_synthetic_plus.json | a Plus plan: primary 5h + secondary weekly windows and an `additional_rate_limits[]` entry |
| codex_usage_wrapped.json | the payload nested under `rate_limits` (`RateLimitStatusWithResetCredits`), string epochs, relative `reset_after_seconds` |
| codex_usage_limited.json | `limit_reached: true` with `rate_limit_reached_type` |
