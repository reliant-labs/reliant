# Title provider routing — 2026-10-24

## Path and root cause

`launch.(*Launcher).startTitleGeneration` starts `generate-title-<chatID>` in a
best-effort goroutine after launch commits. This is a separate Temporal
`GenerateTitleWorkflow`, not a server-side LLM call. The worker executes the
`GenerateTitle` activity with three activity attempts, then writes the truncated
first message if generation fails. A provider usage-limit error is flattened to
a non-retryable error by the activity wrapper, so those three attempts do not
actually run for a 429.

`GenerateTitleActivity.generateTitle` calls `resolveLLMCall`: shared registry,
availability lookup and driver construction, but previously omitted `Routing`.
Consequently it never called `Assign`, never attached the account limit observer,
and never called `credentialRejection` or `usageLimitFailure` on stream failure.
`drivers.GetDriver` selected the provider's default account, the first by priority.
Every title met that account again. This was not a cached provider list:
`LookupAvailableDrivers` reads account and limit rows on each resolution. Signed-out,
paused, unrefreshable expired credentials and availability-rejected credentials
were already filtered there, for titles and main calls alike. Refreshable expired
OAuth credentials deliberately remain eligible: the refresh coordinator refreshes
or retires them. Unknown revoked API keys need the stream failure recorder.

Main calls supply AccountRouting, observe the serving account's headers, and
record failures through credentialRejection / usageLimitFailure; the runtime
consumes their UsageLimitFacts to retry an account, use opt-in fallback or wait.
The durable provider_backoff record is a thread's in-progress HTTP retry marker,
not a provider selection cache or credential bench. Account limited_until is the
selection bench. #1371 clears that same DB state on a confirmed allowed reading;
there is no title-specific cache to invalidate. #1369's open-run query/replay fixes
are unrelated to account selection.

Main has already changed the old combined [fast, moderate] selector into a strict
[fast] then [moderate] ladder. This change preserves it, along with tool choice,
temperature and housekeeping's intentional exclusion of chat tag preferences.

## Read-only production evidence

Queried ClickStack `otel_logs` for worker and API streams and SELECTs inside
`BEGIN READ ONLY` against `reliant_cp`. No prod mutation or deploy.

The observed incident is **usage exhaustion, not expired/revoked credentials**.
All title failures found in the retained October 15–24 window were Anthropic 429
usage-limit refusals; no expired/revoked-title-then-healthy-provider sequence was
found. Titles fell back to the **truncated message**, not another LLM provider.
API logs launch the workflow; the provider calls execute in the worker.

Concrete examples (UTC):

- October 23 18:40:10.882: `[GenerateTitle] LLM generation failed`, chat
  `7aa86e7f-6f69-5378-ac65-2acf0f82ccd1`, Anthropic seven-day refusal,
  `org level disabled`, reset October 28 22:00. Upstream workspace
  `wrkspc_01DMwRsNv3oAdjtVLjrFCxBW`.
- 18:40:10.981: workflow falls back after a **single** non-retryable
  `ProviderUsageLimit` activity attempt. 18:40:11.097 writes truncated text.
- 18:40:12.205: root thread assignment to healthy Claude account
  `86a618d5-7ee9-4790-a928-e5c393256865`. 18:40:15.885: `[CallLLM] Usage`,
  `claude-5.5-opus`, provider `claude`, success true. Same trace
  `7479859ba37566d4e0329e778215678a` as the title failure.
- October 24 01:24:06.532: title for chat
  `ab09bd02-0a25-5927-90ab-1ed78212462c` hits the same upstream workspace's
  seven-day limit and writes fallback at 01:24:07.055. Root assignment at
  01:24:08.467 chooses healthy account `faacd2af-575a-4eb0-80d3-4f480734e0a3`.

Read-only account snapshot: user had nine Claude accounts. Priority-0 account
`c2d08f6e-bf7d-4a91-a423-5baea84f0b0c` remained benched until October 28 22:00,
recorded October 22 21:00, while priorities 6 and 7 were allowed. The old title
path deterministically chooses priority 0; title logs did not emit account ID,
so that identification is an inference from the selection path and snapshot,
not an account ID recorded on the historical title request. The repeated same
workspace refusals also appear October 20 23:21 and 23:22, October 21 20:08,
October 22 21:02 and October 23 17:29.

## Fix and policy

Wire the same worker AccountRouting into titles. Use empty ThreadID: router's
existing non-sticky selection, no read/write of the root conversation's pin.
Install the rate-limit observer and share `providerFailure` across call_llm,
compaction and titles. It retires rejected API keys and benches usage refusals.
Compaction had routing but lacked credential-rejection recording; now it shares
that too. OAuth retirement remains the refresh coordinator's responsibility.

Retry a title on another account when UsageLimitFacts says one is available.
No wait/sleep or full conversation runtime is introduced into the one-shot title.
Owner answered question `d0a8ad05-1b6f-4e1a-8ef9-8f1bdc499a45`: when every
account of the resolved provider is limited, move titles to another connected
provider's fast model. Implement by excluding that provider on re-resolution
through the existing shared selector, never changing chat-turn failover policy.
Attempts are bounded; when nothing can serve, the existing truncated-message
fallback remains. Never auto-disconnect credentials or bind the title to the
chat's model.

## Verification

New tests exercise real registry resolution, real account router with in-memory
store, per-account streaming drivers, bench recording and key retirement:
known limit skipped, healthy title, refusal then healthy same-provider title,
next title skips refusal, revoked key retired, fully limited provider moves to
healthy provider, mid-stream refusal moves providers, all limited makes no
provider call, bounded attempts, and transient failures not account-retried.

`make test-short PKGS=./internal/workflow/runtime/activities/handlers/...` passed.
`go vet ./internal/workflow/runtime/activities/...` passed.
Title/routing/credential/compaction tests passed three consecutive executions.
Full touched packages (`activities` and `activities/handlers`) passed with
`-race -count=1`, respectively 13.943s and 123.994s. Existing DB harnesses use
private test databases; no development data on 5434 was used.

`task ci` cannot start on the unchanged main configuration: pinned Forge rejects
extra check `lint-web` colliding with its own built-in row. Reported to Forge as
1415517609454c0da1c62dca2dacdc3c. Direct affected/build/lint verification follows
rather than modifying unrelated gate configuration in this fix.
