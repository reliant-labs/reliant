# Managed ("reliant" provider) LLM billing — where the price comes from

Read-only investigation, 2026-09-30. Paths are relative to `control-plane/` (CP) or `reliant/` (R).
**V** = verified by reading the code. **I** = inferred.

## TL;DR

1. **What a user is charged is mostly LiteLLM's cost, not ours (V).** The live debit path is `CP internal/llmproxy/billing.go`. It reads LiteLLM's `X-Litellm-Response-Cost` header (`billing.go:57,287`) and debits the wallet by that USD amount (`:360-392`).
2. **This is not passthrough from GCP.** Nothing reads GCP billing: there is no Cloud Billing API, BigQuery export, or `billing_account` anywhere (V). LiteLLM computes the cost from **its own bundled model price map** × tokens. That is list price, not the invoice.
3. **No markup is applied to LLM spend (V).** The only multiplier is infra's 1.35× (`internal/billing/inframeter/shape.go:23`). It is unrelated to LLM spend.
4. **Streaming responses have no cost header.** For those, the proxy falls back to a *third* price table: a hand-written substring table, `fallbackPricing` (`billing.go:711-735`), which defaults to sonnet-tier on a miss. Streaming is the normal case for chat.
5. **The generated `managedReliantPricingSnapshotSeeds` are NOT on the proxy debit path (V).** They are only reached through `ReserveManagedReliantUsage` / `FinalizeManagedReliantUsage` (the BillingGatewayService RPCs, `svcbilling/service.go:1988,2072`). Nothing in `reliant` calls those RPCs; only a vendored proto mentions them (V).
6. **The spend-snapshot worker reconciles against LiteLLM's `/global/spend/keys`** (`internal/llm/spend_snapshot.go:120-135`). Its doc says it is reconciliation-only: it logs drift and does not re-debit.
7. **In reliant, `models.yaml` `cost` feeds client-side cost estimates** (the reliant/openai drivers → `TokenUsage.Cost` → chat state), the catalog RPC and the UI. None of these debit anything.
8. **Removing the catalog would take:**
   - (a) deleting the seeds, the Reserve/Finalize RPCs and the `pricing_snapshots` usage, which is dead-ish;
   - (b) getting a real cost for streaming: LiteLLM `/spend/logs?request_id` after the stream, or deferred settlement from the spend worker;
   - (c) dropping `fallbackPricing`;
   - (d) deciding what reliant shows as per-chat cost.

   Note: truly "passthrough from GCP" would need a billing-export ingestion that does not exist. The realistic option is "passthrough from LiteLLM's price map".
9. The stated design in code comments is "LiteLLM is authoritative, tokens × table is a fallback". The word "passthrough" never appears as a billing design in git or docs (V).
10. The one caveat: LiteLLM's price map is itself a pricing catalog, just one maintained upstream.

## 1. End-to-end path for one managed request

- **reliant side (V).** The `reliant` driver calls the gateway (OpenAI-compatible) and computes a *local* cost from `models.yaml` rates (`R internal/llm/drivers/reliant/driver.go:850-866`). It does not debit anything.
- **Proxy (V).**
  - `CP internal/llmproxy/billing.go:1-14` describes itself as the single in-band meter, replacing the LiteLLM webhook.
  - There is a pre-flight wallet-balance gate (a balance check, not a reservation, per the header comment `:3-5`).
  - `attachMetering` (`:268-307`) wraps the response body.
  - `finishMeter` (`:311-355`) picks the cost:
    - `X-Litellm-Response-Cost` when present (source `litellm_header`);
    - otherwise `costFromUsage` over tokens scanned from the body (`token_estimate`);
    - otherwise nothing, logged as an error (`:328-337`).
  - `debitWallet` (`:360-392`) writes a `usage_debit` ledger entry, idempotent on `(llm_proxy_usage, x-litellm-call-id)`.
- **Token counts (V).** They come from the upstream response `usage` block, which the proxy parses (`billing.go:~680-700`).
- **Price (V).** The price is LiteLLM's (header). When the header is absent, the price comes from `fallbackPricing` (`:711-735`). Its comment says it "mirror[s] svcbilling's managedReliantPricingSnapshotSeeds", but that copy is hand-made and not generated.
- **Markup (V).** None on this path: the amount is `-costUSD` scaled (`:364-366`).
- **Reservations and settlement.** The Reserve/Finalize path exists in svcbilling. gopls callers of `resolveManagedReliantPricingSnapshot`: `ReserveManagedReliantUsage` (`service.go:1988`) and `FinalizeManagedReliantUsage` (`service.go:2072`). Their only callers are handlers `internal/handlers/billing_gateway/handlers.go:152,163`, i.e. the connect RPC.
  - No caller in `reliant` (V: only `proto-vendor/.../billing.proto:21` mentions it).
  - `internal/ratelimit/interceptor.go:112-118` has a stale note that the service "has not been ported" (I: the comment has gone stale).

## 2. GCP billing

Searched both repos for `cloudbilling|billing_account|billingaccount|bigquery|billing export` (V). The only hits are:

- `reliant/go.work.sum` bigquery module checksums (transitive);
- unrelated `useCloudBillingQueries.ts` mentions in specs.

Nothing ingests GCP billing and nothing reconciles against it.

## 3. LiteLLM's cost

- **Header (V).** The cost header is used as the authoritative source (`billing.go:50-57, 287, 314-317`).
- **Spend DB (V).** LiteLLM's spend DB (`deploy/litellm/config.yaml:6,88-92`) is read by:
  - `internal/litellm/client.go:390` (`/global/spend/keys`), `:400` (`/spend/logs?api_key=`) and `:427` (`/global/spend/teams`);
  - the snapshot reconciler `internal/llm/spend_snapshot.go:120-135`;
  - usage display in `internal/llm/service.go:560`.
- **Price source (V).** `deploy/litellm/config.yaml` has no custom `input_cost_per_token` (grep for `cost` found none). So LiteLLM uses its built-in map for `vertex_ai/<model>` (I: LiteLLM default behavior).

## 4. What depends on the seeds / snapshot

- **Seeds (V).**
  - `svcbilling/pricing_seeds_catalog.go:24` is generated by `internal/llmgateway/render.go:290-310`.
  - `directProviderPricingSnapshotSeeds` (`service.go:167-180`) is hand-maintained.
  - Both are consumed only by `lookupManagedReliantPricingSnapshotSeed` (`service.go:2290-2310`).
  - On a miss, the lookup returns `InvalidArgument` (`service.go:2263-2266`). Only Reserve/Finalize are affected, not proxy traffic.
- **Table (V).** `controlplane.pricing_snapshots` (`db/migrations/00038_canonical_ai_billing_platform.up.sql:2-15`) is lazily upserted with `pricing_version = "managed-reliant-static-v1"` (`service.go:2275-2288`).
- **Other readers (V).** The table is also referenced by `internal/billing/pricing/service.go:18` and `svcbilling/canonical_metering.go:38,120,240`, which carry `pricing_snapshot_id` on reservations and usage events. Migration `00071_resource_usage_buckets` also mentions it.
- **Not checked.** Removal needs a roll-forward migration; no down migration, per policy. I did not verify the admin UI or Stripe metering for price display. The proxy writes `usage_records` metric `llm_spend` (`billing.go:46-47`) as USD, so Stripe/invoice would use dollars and not prices (I).

## 5. reliant consumers of `models.yaml` `cost`

All verified:

| Consumer | Location | What it does with the cost |
|---|---|---|
| Model struct | `R internal/llm/models/types.go:320-322` | Maps it to `Model.CostPer1MIn/Out/InCached` (`model.go:55-57`) |
| reliant driver | `R internal/llm/drivers/reliant/driver.go:857-859` | Computes `TokenUsage.Cost` |
| openai driver | `R internal/llm/drivers/openai/driver.go:579-586` (called at `:575,873,1049`) | Computes `TokenUsage.Cost` |
| Chat state | `R internal/workflow/runtime/activities/handlers/call_llm.go:2715` | Stores `state.cost` (per-chat cost display/budget; I) |
| Catalog RPC | `R internal/grpc/services/catalog.go:125-126, 327-328, 372-373` | Returns the prices to the UI (`web/src/api/client.ts:87,220`) |
| Gateway generator | `R pkg/llmcatalog/catalog.go:62-78, 205-207` | Feeds CP's generator |

## 6. History and intent

- **`git log -S managedReliantPricingSnapshotSeeds` (CP)** returns: `36b41c23 Initial commit from forge`, `eb715aea port: svcbilling fail-closed`, `299952d6 updates around billing`, `f9ea5aed feat(llm): generate the gateway model artifacts from reliant's catalog (#217)`.
- **`-S pricing_seeds`** returns `f9ea5aed` and `d68b61e5`.
- **`--grep=passthrough`** has no billing-related hits.
- **Docs.** `rg passthrough` in `docs/`, `R specs`, `R docs` found no billing design statement.
- **Stated design** (quote, `billing.go:52-56`): "x-litellm-response-cost is reliably present only for NON-streaming responses … we fall back to computing cost from the streamed usage tokens".
