# 01d — Inference and Evals

<!--
  prettier-ignore: prettier 3.5 reads the unquoted metric names in this file
  (node_edac_*, num_retracted_*, …) as emphasis and rewrites them, and joins
  adjacent code spans, so this file is left as researched.
-->

<!-- prettier-ignore-start -->

Slice: inference-server metrics, OTel GenAI semconv, request-trace design, evals data model, model registry metadata.
Confidence: (V) = checked against an upstream source this session (see Sources), (K) = knowledge. Fetched 2026-11-26.

**Headline corrections to earlier notes**
- (V) The OTel GenAI conventions **moved** out of `open-telemetry/semantic-conventions` into a new repo, `open-telemetry/semantic-conventions-genai`. The old pages say "Moved… no longer maintained". Everything is still **Development** stability. The client token metric was split up: the old single `gen_ai.client.token.usage` histogram (with a `gen_ai.token.type` attribute) is now a set of `gen_ai.client.inference.usage.*` counters plus `gen_ai.client.inference.operation.*_tokens` histograms. Ingest both the old and new names.
- (V) vLLM's OTLP tracing still emits **its own pre-semconv attribute names** (`gen_ai.usage.prompt_tokens`, `gen_ai.latency.*`), not the current semconv names (`gen_ai.usage.input_tokens`). We need an alias map.
- (V) vLLM main has grown a lot past the familiar set: waiting-by-reason, admission rejections, KV block lifetime/reuse histograms, cached prompt tokens by source, per-request preemptions, MFU estimates (`estimated_flops_per_gpu_total`), and engine sleep state.

---

## 1. Inference server metrics

### 1.1 vLLM (V1 engine) — Prometheus `/metrics`

Labels on every engine metric: `model_name` (served model name) and `engine` (the data-parallel engine index) (V). Metrics are on by default when you run `vllm serve`. Some are hidden or opt-in: `--show-hidden-metrics-for-version` keeps deprecated names alive, `kv_cache_metrics` turns on the block-lifetime histograms, and `enable_mfu_metrics` turns on the perf metrics (V).

| Name | Source | Type & unit | Granularity / labels | Default on? | Why it matters | Used by view | When absent |
|---|---|---|---|---|---|---|---|
| `vllm:num_requests_running` | vllm v1 loggers.py (V) | gauge, reqs | model_name, engine | yes | Current batch occupancy | Inference service | `vllm-metrics-not-scraped`: add a PodMonitor or OTel scrape on :8000/metrics |
| `vllm:num_requests_waiting` | (V) | gauge | model_name, engine | yes | Queue depth; the main autoscaling signal | Inference service, Queues | same |
| `vllm:num_requests_waiting_by_reason` | (V), new | gauge | + `reason`=capacity\|deferred | yes | Tells you whether queueing comes from capacity or from LoRA budget / KV transfer | Inference service | `vllm-old-version`: upgrade |
| `vllm:num_requests_swapped` | V0 only (K) | gauge | — | removed in V1 | Legacy swap to CPU | — | ignore |
| `vllm:num_requests_kv_fetch_by_stage` | (V) | gauge | + `stage`=waiting_to_start\|in_progress\|completed_waiting | only when a KV consumer (disaggregated) | Decode side stuck waiting on KV from prefill | Inference service | — |
| `vllm:admission_rejections` | (V) | counter | + reason=max_num_queued_reqs\|max_num_queued_tokens | yes (frontend) | Load shedding (429s) | Inference service | — |
| `vllm:kv_cache_usage_perc` | (V); was `gpu_cache_usage_perc` (K) | gauge, 0–1 (fraction, despite the name) | model_name, engine | yes | KV pressure that leads to preemption | Inference service, GPU detail | alias the old name |
| `vllm:prefix_cache_queries` / `vllm:prefix_cache_hits` | (V) | counters, tokens | model_name, engine | yes | Prefix-cache hit rate = hits/queries | Inference service | `prefix-caching-off` |
| `vllm:external_prefix_cache_queries/hits` | (V) | counters | — | with a KV connector (LMCache etc.) | Hit rate of the offloaded cache | Inference service | — |
| `vllm:mm_cache_queries/hits` | (V) | counters | — | multimodal models | Multimodal encoder cache | Inference service | — |
| `vllm:num_preemptions` | (V) | counter | — | yes | KV thrash; raise memory or lower max_num_seqs | Inference service, Recommendations | `kv-preemptions-high` |
| `vllm:request_num_preemptions` | (V), new | histogram | per request | yes | Distribution of preemptions per request | Inference request trace | — |
| `vllm:prompt_tokens` | (V) | counter, tokens | — | yes | Input throughput | Inference service, Cost | — |
| `vllm:prompt_tokens_by_source` | (V), new | counter | + source (computed / local cache / external) | yes | How much prefill was actually computed | Inference service | — |
| `vllm:prompt_tokens_cached`, `..._cached_by_source` | (V) | counter | — | yes | Cached input tokens (price discount) | Cost | — |
| `vllm:generation_tokens` | (V) | counter, tokens | — | yes | Output throughput | Inference service, Cost | — |
| `vllm:request_success` | (V) | counter | + `finished_reason`=stop\|length\|abort (+ error in newer versions (K)) | yes | Error rate and truncation rate | Inference service | — |
| `vllm:corrupted_requests` | (V) | counter | — | with `VLLM_COMPUTE_NANS_IN_LOGITS` | NaN logits (numerics bug) | Inference service | — |
| `vllm:request_prompt_tokens` / `vllm:request_generation_tokens` | (V) | histograms, tokens | per request | yes | Request shape; sizing max_model_len | Inference service | — |
| `vllm:request_params_n` / `vllm:request_params_max_tokens` / `vllm:request_max_num_generation_tokens` | (V) | histograms | per request | yes | Client behaviour | Inference service | — |
| `vllm:request_prefill_kv_computed_tokens` | (V), new | histogram | per request | yes | Real prefill work after cache hits | Inference service | — |
| `vllm:iteration_tokens_total` | (V) | histogram, tokens per step | per engine step | yes | Effective batch size in tokens | Inference service | — |
| `vllm:time_to_first_token_seconds` | (V) | histogram, s | — | yes | TTFT SLI | Inference service | — |
| `vllm:inter_token_latency_seconds` | (V); was `time_per_output_token_seconds` (K) | histogram, s | per token gap | yes | ITL SLI | Inference service | alias |
| `vllm:request_time_per_output_token_seconds` | (V) | histogram, s | per request mean | yes | TPOT (per request) — not the same as ITL | Inference service | — |
| `vllm:e2e_request_latency_seconds` | (K; in the docs) | histogram, s | — | yes | e2e | Inference service | — |
| `vllm:request_queue_time_seconds` | (V) | histogram, s | — | yes | Queue wait | Inference service, Queues | — |
| `vllm:request_inference_time_seconds` / `_prefill_time_seconds` / `_decode_time_seconds` | (V) | histograms, s | — | yes | Splits latency into prefill and decode | Inference service | — |
| `vllm:spec_decode_num_drafts` / `_num_draft_tokens` / `_num_accepted_tokens` / `_num_verified_tokens` | spec_decode/metrics.py (V) | counters | — | only with a speculative config | Acceptance rate = accepted/draft | Inference service | `spec-decode-low-accept` |
| `vllm:spec_decode_num_accepted_tokens_per_pos` / `_verified_tokens_per_pos` | (V) | counter | + `position` | spec | Acceptance falloff by draft position | Inference service | — |
| `vllm:lora_requests_info` | (V) | gauge (info-style; value = timestamp) | labels `running_lora_adapters`, `waiting_lora_adapters`, `max_lora` (K) | with `--enable-lora` | Which adapters are hot | Inference service, Model version | — |
| `vllm:cache_config_info` | (V) | info gauge | labels = cache config (block_size, num_gpu_blocks, gpu_memory_utilization, cache_dtype, enable_prefix_caching…) | yes | Config provenance; num_gpu_blocks is the KV capacity | Inference service, Model version | — |
| `vllm:kv_block_lifetime_seconds` / `_idle_before_evict_seconds` / `_reuse_gap_seconds` | (V) | histograms | — | opt-in `kv_cache_metrics` | Cache sizing | Recommendations | — |
| `vllm:watermark_requests` | (V) | gauge/counter | — | yes | Requests held by the KV watermark | Inference service | — |
| `vllm:engine_sleep_state` | (V) | gauge | + awake/level | yes | Sleep mode (RL weight-swap) | Inference service | — |
| `vllm:estimated_flops_per_gpu_total`, `_read_bytes_`, `_write_bytes_` | perf.py (V) | counters | — | opt-in `enable_mfu_metrics` | MFU / bandwidth utilisation | Inference service, GPU detail | `vllm-mfu-off` |
| `vllm:diffusion_*` | (V) | — | — | diffusion LMs | — | — | — |
| KV-connector metrics (NIXL etc.) | kv_connector metrics (V, module only) | — | — | disaggregated serving | Transfer latency | Inference service | — |

**vLLM OTLP tracing** (V for attribute names): turn it on with `--otlp-traces-endpoint <url>` and `--collect-detailed-traces model,worker,all` (K for the flag values; detailed traces add overhead). vLLM emits one `llm_request` span per request (K), with these attributes: `gen_ai.request.id`, `gen_ai.response.model`, `gen_ai.request.max_tokens`, `.temperature`, `.top_p`, `.n`, `gen_ai.usage.prompt_tokens`, `gen_ai.usage.completion_tokens`, `gen_ai.usage.num_sequences`, and the latency breakdown `gen_ai.latency.time_in_queue`, `.time_to_first_token`, `.time_in_scheduler`, `.time_in_model_prefill`, `.time_in_model_decode`, `.time_in_model_inference`, `.time_in_model_forward`, `.time_in_model_execute` (the last two are from detailed traces). It propagates `traceparent` from the incoming HTTP request (K). Gap: these names are not the current semconv names, so normalise `prompt_tokens`→`input_tokens` and `completion_tokens`→`output_tokens`.

### 1.2 SGLang — `--enable-metrics`, `sglang:*` (V names, from metrics_collector.py)
Metrics are **off by default** (need `--enable-metrics`) (K). Labels: `model_name`, plus tp_rank/dp_rank and others (K).

| Group | Metrics (V) | Neutral SLI |
|---|---|---|
| Load | `num_running_reqs`, `num_queue_reqs`, `num_used_tokens`, `token_usage`, `full_token_usage`, `swa_token_usage`, `utilization`, `num_paused_reqs`, `num_retracted_reqs`, `num_retracted_requests_total`, `num_retracted_input/output_tokens_total`, `max_total_num_tokens`, `kv_used_tokens`, `kv_available_tokens`, `kv_evictable_tokens` | batch, KV util, preemption (SGLang calls it "retract") |
| Cache | `cache_hit_rate`, `cached_tokens_total`, `evicted_tokens_total`, `eviction_duration_seconds`, `hicache_*` (host-tier cache), `storage_prefetch_*` | prefix-cache hit rate, evictions |
| Latency | `time_to_first_token_seconds`, `inter_token_latency_seconds`, `request_time_per_output_token_seconds`, `queue_time_seconds`, `per_stage_req_latency_seconds`, e2e (`e2e_request_latency_seconds` (K)) | TTFT/ITL/TPOT/queue/e2e |
| Throughput | `gen_throughput` (gauge tok/s), `prompt_tokens_total`, `generation_tokens_total`, `prompt/generation_tokens_histogram`, `realtime_tokens_total`, `num_requests_total`, `num_aborted_requests_total` | tokens/s, error rate |
| Spec decode | `spec_accept_length`, `spec_accept_rate`, `spec_num_draft_tokens`, `spec_verify_calls_total` | acceptance |
| Disaggregation | `num_prefill_bootstrap_queue_reqs`, `num_prefill_inflight_queue_reqs`, `num_decode_prealloc_queue_reqs`, `num_decode_transfer_queue_reqs`, `kv_transfer_latency_ms`, `kv_transfer_speed_gb_s`, `num_transfer_failed_reqs_total`, `num_bootstrap_failed_reqs_total` | P/D transfer health |
| LoRA | `lora_pool_slots_total/used`, `lora_pool_utilization` | per-adapter capacity |
| Grammar (structured output) | `num_grammar_total`, `grammar_compilation_time_seconds`, `num_grammar_timeout_total`, `num_grammar_cache_hit_total` | structured-output latency |
| Memory/startup | `weight_memory_usage_gb`, `kv_cache_memory_usage_gb`, `graph_memory_usage_gb`, `startup_time_seconds`, `weight_load_duration_seconds`, `startup_cuda_graph_time_seconds` | Pod start, GPU memory |
| MoE / DP | `eplb_balancedness`, `dp_attention_token_imbalance_ratio`, `estimated_flops_per_gpu_total` | expert imbalance, MFU |

Recommendation when absent: `sglang-metrics-off` → add `--enable-metrics`.

### 1.3 TGI (Hugging Face text-generation-inference) — router `/metrics` (V names, router/src/server.rs)
On by default. TGI is in maintenance mode upstream (K).

| Name | Type & unit | Neutral SLI |
|---|---|---|
| `tgi_request_count`, `tgi_request_success`, `tgi_request_failure` (label `err`: validation/generation/overloaded/…, K) | counters | traffic, error by reason |
| `tgi_request_duration`, `tgi_request_validation_duration`, `tgi_request_queue_duration`, `tgi_request_inference_duration`, `tgi_request_mean_time_per_token_duration` | histograms, s | e2e, queue, inference, TPOT |
| `tgi_request_input_length`, `tgi_request_generated_tokens`, `tgi_request_max_new_tokens`, `tgi_request_skipped_tokens` | histograms, tokens | request shape |
| `tgi_batch_current_size`, `tgi_batch_current_max_tokens`, `tgi_batch_next_size`, `tgi_batch_total_tokens` | gauges / histograms | batch size, KV budget |
| `tgi_batch_inference_count`, `tgi_batch_inference_success`, `tgi_batch_inference_duration`, `tgi_batch_forward_duration`, `tgi_batch_decode_duration`, `tgi_batch_filter_duration` (label `method`=prefill\|decode, K) | counters/histograms | prefill vs decode step time |
| `tgi_queue_size` | gauge | queue depth |

TGI has no TTFT histogram (K): derive TTFT from queue + prefill batch duration, or measure it client-side.

### 1.4 NVIDIA Triton (V names from docs/user_guide/metrics.md) — `:8002/metrics`
On by default. Labels: `model`, `version`; GPU metrics carry `gpu_uuid` (K). Latency metrics are **cumulative µs counters** by default; histograms (`*_histogram_us`, `nv_inference_first_response_histogram_ms`) and summaries (`--metrics-config summary_latencies=true`, quantiles) are opt-in (K).

| Name | Type | Neutral SLI |
|---|---|---|
| `nv_inference_request_success`, `nv_inference_request_failure` (label `reason`=REJECTED\|CANCELED\|BACKEND\|OTHER, K) | counter | error rate by reason |
| `nv_inference_count` (inferences, counts batch items), `nv_inference_exec_count` (batch executions) | counter | avg batch = count/exec_count |
| `nv_inference_pending_request_count` | gauge | queue depth |
| `nv_inference_request_duration_us`, `nv_inference_queue_duration_us`, `nv_inference_compute_input_duration_us`, `nv_inference_compute_infer_duration_us`, `nv_inference_compute_output_duration_us` | counter µs (divide by Δsuccess for the mean) | e2e, queue, compute |
| `nv_inference_*_histogram_us`, `nv_inference_first_response_histogram_ms` (decoupled/streaming models: TTFT) | histogram (opt-in) | percentiles, TTFT |
| `nv_cache_num_hits_per_model`, `nv_cache_num_misses_per_model`, `nv_cache_hit/miss_duration_per_model` (+ histogram/summary variants) | counter | response-cache hit rate |
| `nv_gpu_utilization`, `nv_gpu_memory_total_bytes`, `nv_gpu_memory_used_bytes`, `nv_gpu_power_usage`, `nv_gpu_power_limit`, `nv_energy_consumption` (J counter) | gauge/counter, per GPU | prefer DCGM; this is a fallback |
| `nv_cpu_utilization`, `nv_cpu_memory_used/total_bytes`, `nv_pinned_memory_pool_used/total_bytes` | gauge | host pressure |

**TensorRT-LLM backend** (V label values from the tensorrtllm_backend README): `nv_trt_llm_request_metrics{request_type=active|max|scheduled|context|waiting}`, `nv_trt_llm_kv_cache_block_metrics{kv_cache_block_type=max|free|used|tokens_per|fraction}`, `nv_trt_llm_inflight_batcher_metrics{inflight_batcher_specific_metric=total_context_tokens|generation_requests|micro_batch_id|…}`, `nv_trt_llm_runtime_memory_metrics`, `nv_trt_llm_general_metrics{general_type=iteration_counter|timestamp}`, `nv_trt_llm_disaggregated_serving_metrics`. These are gauges set per iteration. KV utilisation = used/max blocks. The tokens-per-block value gives token capacity.

### 1.5 NVIDIA NIM / Dynamo (K, brief)
- NIM for LLMs wraps vLLM, SGLang or TRT-LLM and re-exposes their metrics (vLLM-style names, `num_requests_running`, `gpu_cache_usage_perc`, TTFT/ITL histograms) on `/v1/metrics`. Detect the engine from the image and model-profile labels.
- Dynamo is disaggregated serving: a frontend, a KV-aware router, prefill workers and decode workers. It exposes `dynamo_frontend_*` (requests, TTFT, ITL, inflight, queued, input/output sequence tokens) and `dynamo_component_*` (per worker), plus KV router events (KV hit-rate on routing, overlap blocks). In the UI show prefill and decode as two pools, with the KV transfer (NIXL) latency between them. Unverified this session: confirm the metric prefixes before implementation.

### 1.6 KServe / Knative / llm-d / Gateway API Inference Extension / LWS (K unless marked)
| Thing | What to read | Neutral SLI / why |
|---|---|---|
| KServe `InferenceService` status | `.status.conditions` (Ready, PredictorReady, IngressReady, LatestDeploymentReady), `.status.url`, `.status.components.predictor.{latestReadyRevision,latestCreatedRevision,traffic[]{revisionName,percent,latestRevision,tag}}`, `.status.modelStatus.{states.activeModelState, transitionStatus, lastFailureInfo{reason,message,exitCode}}` | rollout/canary state → Model version, release ledger; failure reason → Pod start |
| KServe `LLMInferenceService` (newer, llm-d based) | spec model URI, parallelism, router/scheduler refs; status conditions | same |
| Knative autoscaler (KPA) | `autoscaler_desired_pods`, `autoscaler_actual_pods`, `autoscaler_requested_pods`, `autoscaler_stable_request_concurrency`, `autoscaler_panic_request_concurrency`, `autoscaler_target_concurrency_per_pod`, `autoscaler_panic_mode` (0/1), `activator_request_concurrency`, `activator_request_count`, `activator_request_latencies` (names changed with Knative's OTel migration in 1.18+; verify) | scale-to-zero cold starts, panic bursts → Pod start, Inference service |
| GIE EndpointPicker (EPP) | `inference_objective_request_total` (was `inference_model_request_total` when the CRD was InferenceModel; renamed with the InferenceObjective API), `inference_objective_request_error_total`, `inference_objective_request_duration_seconds`, `inference_objective_input_tokens`, `inference_objective_output_tokens`, `inference_objective_request_sizes`, `inference_objective_running_requests`, `inference_pool_average_kv_cache_utilization`, `inference_pool_average_queue_size`, `inference_pool_per_pod_queue_size`, `inference_pool_ready_pods`, `inference_extension_scheduler_e2e_duration_seconds`, plugin duration metrics. Labels: `model_name`, `target_model_name`, `name` (pool) | routing decision latency, per-objective (criticality) traffic, pool saturation; unverified this session (the doc URL 404'd) |
| llm-d | EPP above + prefix-cache-aware scorer, P/D disaggregation via vLLM NIXL connector; reuses vLLM metrics | — |
| LeaderWorkerSet | `LeaderWorkerSet` status (replicas, readyReplicas, conditions Available/Progressing/UpdateInProgress), group pods labelled `leaderworkerset.sigs.k8s.io/name`, `/group-index`, `/worker-index` | multi-host (TP across nodes) serving: one replica = N pods; health = every worker ready → Inference service topology |
| Ray Serve | `ray_serve_num_http_requests`, `ray_serve_http_request_latency_ms`, `ray_serve_num_http_error_requests`, `ray_serve_deployment_request_counter`, `ray_serve_deployment_processing_latency_ms`, `ray_serve_deployment_queued_queries`, `ray_serve_replica_processing_queries`, `ray_serve_num_ongoing_http_requests`, `ray_serve_deployment_error_counter`, `ray_serve_num_router_requests`; Ray Serve LLM re-exports vLLM metrics with the prefix `ray_vllm:` (verify) | per-deployment latency/queue |

### 1.7 Neutral SLI mapping
| Neutral SLI | vLLM | SGLang | TGI | Triton / TRT-LLM | GIE/EPP | OTel semconv |
|---|---|---|---|---|---|---|
| Queue wait | request_queue_time_seconds | queue_time_seconds | tgi_request_queue_duration | nv_inference_queue_duration_us | — | — |
| Queue depth | num_requests_waiting | num_queue_reqs | tgi_queue_size | nv_inference_pending_request_count / trt request_type=waiting | inference_pool_average_queue_size | — |
| TTFT | time_to_first_token_seconds | time_to_first_token_seconds | derived | nv_inference_first_response_histogram_ms | — | gen_ai.server.time_to_first_token |
| ITL | inter_token_latency_seconds | inter_token_latency_seconds | — | — | — | (client: time_per_output_chunk) |
| TPOT | request_time_per_output_token_seconds | request_time_per_output_token_seconds | tgi_request_mean_time_per_token_duration | — | — | gen_ai.server.time_per_output_token |
| e2e | e2e_request_latency_seconds | e2e_request_latency_seconds | tgi_request_duration | nv_inference_request_duration_us | inference_objective_request_duration_seconds | gen_ai.server.request.duration |
| Prefill / decode time | request_prefill/decode_time_seconds | per_stage_req_latency_seconds | tgi_batch_forward_duration{method} | compute_infer (combined) | — | — |
| Tokens in/out per s | rate(prompt_tokens / generation_tokens) | rate(*_tokens_total), gen_throughput | Σ histogram sums | — | input/output_tokens | gen_ai.client.inference.usage.* |
| Batch size | num_requests_running, iteration_tokens_total | num_running_reqs | tgi_batch_current_size | count/exec_count, request_type=scheduled | running_requests | — |
| KV util | kv_cache_usage_perc | token_usage | — | used/max blocks | inference_pool_average_kv_cache_utilization | — |
| Preemption / eviction | num_preemptions | num_retracted_*, evicted_tokens_total | — | — | — | — |
| Prefix-cache hit | prefix_cache_hits/queries | cache_hit_rate | — | nv_cache_* (response cache, different) | — | cache_read.input_tokens / input_tokens |
| Spec acceptance | accepted/draft tokens | spec_accept_rate, spec_accept_length | — | — | — | — |
| Errors by reason | request_success{finished_reason}, admission_rejections | num_aborted_requests_total, grammar timeouts | tgi_request_failure{err} | request_failure{reason} | request_error_total{error_code} | error.type |
| Per-adapter traffic | lora_requests_info (+ model_name = adapter on requests) | lora_pool_* | adapter_id label (K) | — | target_model_name | gen_ai.request.model |
| GPU memory | DCGM FB_USED (+ cache_config_info) | weight/kv/graph_memory_usage_gb | — | nv_gpu_memory_used_bytes | — | — |
| Goodput | derived | derived | derived | derived | — | — |
| Cost / 1M tokens | derived | derived | derived | derived | — | — |

OOM and timeout have no engine counter. OOM shows up as a pod restart (`OOMKilled`, or a CUDA OOM line in the log). Timeout shows up as `abort` plus a client/gateway 504. Join those signals in the "errors by reason" view.

---

## 2. OpenTelemetry GenAI semantic conventions (V; all Development)

The conventions now live in `open-telemetry/semantic-conventions-genai`. Opt-in to the newer attribute names with `OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental` (K).

**Metrics**
| Name | Instrument | Unit | Notes |
|---|---|---|---|
| `gen_ai.client.operation.duration` | Histogram | s | Generic operation; attributes operation.name, provider.name, request.model, response.model, server.address, error.type |
| `gen_ai.client.inference.duration` | Histogram | s | New, inference-specific |
| `gen_ai.client.inference.time_to_first_chunk` | Histogram | s | Client-side TTFT |
| `gen_ai.client.inference.time_per_output_chunk` | Histogram | s | Per chunk after the first |
| `gen_ai.client.inference.usage.input_tokens` | Counter | {token} | Includes cached tokens |
| `gen_ai.client.inference.usage.output_tokens` | Counter | {token} | Includes reasoning tokens |
| `gen_ai.client.inference.usage.cache_read.input_tokens` / `.cache_write.input_tokens` | Counter | {token} | Provider cache |
| `gen_ai.client.inference.usage.reasoning.output_tokens` | Counter | {token} | |
| `gen_ai.client.inference.operation.input_tokens` / `.output_tokens` | Histogram | {token} | Per-operation distribution |
| `gen_ai.client.token.usage` | Histogram | {token} | **Legacy**, with `gen_ai.token.type`=input\|output (K); still widely emitted (openllmetry, older SDKs). Ingest it and map it |
| `gen_ai.client.embeddings.duration` | Histogram | s | |
| `gen_ai.server.request.duration` | Histogram | s | Time to last byte / last token |
| `gen_ai.server.time_to_first_token` | Histogram | s | Successful responses only |
| `gen_ai.server.time_per_output_token` | Histogram | s | After the first token, successful responses only |
| `gen_ai.invoke_agent.duration` | Histogram | s | Agent end-to-end |
| `gen_ai.invoke_agent.inference_calls`, `gen_ai.invoke_agent.tool_calls` | Histogram | {inference_call}/{tool_call} | |
| `gen_ai.execute_tool.duration` | Histogram | s | |

**Span attributes (inference span; span name `{operation} {request.model}`, kind CLIENT)**: `gen_ai.operation.name` (required: chat, text_completion, generate_content, embeddings, execute_tool, invoke_agent, create_agent, invoke_workflow, plan, retrieval, realtime_inference, *_memory…), `gen_ai.provider.name` (required; this **replaced `gen_ai.system`** (K)), `gen_ai.request.model`, `gen_ai.response.model`, `gen_ai.response.id`, `gen_ai.response.finish_reasons[]`, `gen_ai.request.{max_tokens,temperature,top_p,top_k,frequency_penalty,presence_penalty,seed,stop_sequences,choice.count,stream,reasoning.level,service_tier}`, `gen_ai.response.service_tier`, `gen_ai.response.time_to_first_chunk`, `gen_ai.usage.{input_tokens,output_tokens,cache_read.input_tokens,cache_write.input_tokens,reasoning.output_tokens}` plus per-modality (`text.`, `image.`, `audio.`), `gen_ai.conversation.id`, `gen_ai.conversation.compacted`, `gen_ai.prompt.{name,version,variables}`, `gen_ai.output.type`, `gen_ai.request.previous_response.id`, `server.address/port`, `error.type`.
**Content (opt-in, off by default)**: `gen_ai.input.messages`, `gen_ai.output.messages`, `gen_ai.system_instructions`, `gen_ai.tool.definitions`, plus `*_ref` variants that point to externally stored content. These replaced the per-message events (`gen_ai.user.message`, `gen_ai.choice`, … (K)) that older SDKs emit.
**Agent/tool spans**: `invoke_agent` / `create_agent` with `gen_ai.agent.{id,name,description,version}`, `gen_ai.data_source.id`; `execute_tool` with `gen_ai.tool.{name,call.id,type,call.arguments,call.result}`; memory spans with `gen_ai.memory.*`; retrieval spans with `gen_ai.retrieval.query.text`.
**Evaluation event** `gen_ai.evaluation.result`: `gen_ai.evaluation.name`, `gen_ai.evaluation.score.value`, `gen_ai.evaluation.score.label`, `gen_ai.evaluation.explanation`, `gen_ai.response.id` (links the score to the response). This is the native carrier for **online evals**: adopt it as the wire format.
**Provider docs** exist for openai, anthropic, aws-bedrock, azure-ai-inference and MCP (`mcp.*` spans).

---

## 3. Request-level trace design (K, design)

```
app span (gen_ai chat, CLIENT)  ── traceparent ──▶
  gateway span (Envoy/GIE, SERVER)  attrs: route, pool, objective, criticality
    epp.schedule span               attrs: candidates, chosen_pod, scores{kv,queue,prefix,lora}, filter_chain, duration
  model-server span (vLLM llm_request, SERVER)  attrs: gen_ai.request.id, model, usage, sampling params
    queue      (time_in_queue)            ← synthesised child from latency attrs
    prefill    (time_in_model_prefill; prompt tokens, cached tokens)
    decode     (time_in_model_decode; output tokens; preemptions; spec accept)
      decode chunk events (optional, sampled: per N tokens; ITL outliers)
    detokenize/stream response (TTFT mark event, finish_reason)
```
- vLLM gives the phase durations as attributes, not child spans. On ingest we **synthesise** child spans (queue → prefill → decode) from start time + durations, so the waterfall reads naturally. Flag them `synthetic=true`.
- Join: the W3C `traceparent` propagated through the gateway (Envoy has native OTel; EPP is an ext-proc filter and must propagate it (K)). Fallback join keys when tracing is broken: `gen_ai.response.id` / `x-request-id` (vLLM echoes the request id), plus time window and pod.
- Exemplars: attach trace_id to TTFT/e2e histogram buckets (OpenMetrics exemplars; vLLM does not emit them (K)). So we derive "slowest N requests per minute" from the span table, linked from each p99 chart point.
- Request-trace view columns: queue, TTFT, prefill, decode, ITL p50/max, tokens in/cached/out, finish_reason, preemptions, chosen pod and why, adapter, GPU(s) (pod → DCGM join), cost.
- Sampling: tail-sample keep-all for errors, finish_reason=length/abort, and anything over the SLO; head-sample the rest at 1–10%. Content capture stays off unless the org opts in, and redaction runs at the collector.

---

## 4. Evals

### 4.1 Neutral data model
- **EvalSuite** (name, version, tasks[]) → **Task** (name, version/hash, dataset uri + revision + split, n_shots, prompt template hash, metric list, filters, generation params).
- **EvalRun** (id, model_version ref — the registry digest or checkpoint step, run ref for eval-in-training, harness+version, git sha, seed, hardware, started/ended, tokens in/out, $ cost, wall time, judge config).
- **Score** (eval_run, task, metric, filter, value, stderr, ci_low/ci_high, n, higher_is_better, slice key/values).
- **Sample** (eval_run, task, sample_id/doc_id, input ref, target, output, per-metric scores, judge rationale, tokens, latency, error, trace_id).
- **Comparison** (baseline run, candidate run, metric, delta, test (paired bootstrap / McNemar / permutation), p, significant flag).
- **HumanRating** (sample, rater, label/score, rubric version) → agreement stats.
- Online: `gen_ai.evaluation.result` events + user feedback (thumbs, task success) keyed on `gen_ai.response.id`.

### 4.2 Framework output formats
| Framework | Output shape | Ingest notes |
|---|---|---|
| lm-evaluation-harness | `results_<ts>.json`: `results.<task>.{"<metric>,<filter>": v, "<metric>_stderr,<filter>": se, alias}` (e.g. `"acc,none"`, `"exact_match,strict-match"`), `groups`, `group_subtasks`, `configs.<task>` (full task yaml), `versions`, `n-shot`, `higher_is_better`, `n-samples.<task>.{original,effective}`, `config` (model, model_args, batch_size, device, limit), `git_hash`, `date`, `model_name`, `model_name_sanitized`, `total_evaluation_time_seconds`, tokenizer/chat-template fields; `--log_samples` → `samples_<task>_<ts>.jsonl` with `doc_id`, `doc`, `target`, `arguments`, `resps`, `filtered_resps`, `filter`, `metrics`, `doc_hash`, `prompt_hash`, `target_hash`, and per-metric values (K, mature format) | split the "metric,filter" key; stderr → CI = ±1.96·se; native W&B/HF hub loggers exist |
| HELM | `benchmark_output/runs/<suite>/<run_spec>/{run_spec.json, scenario_state.json, per_instance_stats.json, stats.json}`; stats = list of `{name{name,split,perturbation}, count, sum, mean, min, max, stddev}` (K) | perturbation = robustness slice |
| OpenAI simple-evals | per-eval HTML + JSON with `score`, `metrics`, and `*_stderr` / bootstrap std (K); openai/evals (legacy): jsonl event log with `spec`, `final_report`, sampling events | |
| Inspect AI (UK AISI) | `.eval` log (zip of JSON; `.json` also supported): `eval` (task, task_version, model, dataset{name,location,samples,sample_ids}, config, revision{git}, packages), `plan` (solvers, generate config), `results` (total_samples, completed_samples, `scores[]` {name, scorer, reducer, metrics{accuracy, stderr, mean…}}), `stats` (started_at, completed_at, `model_usage{model:{input_tokens,output_tokens,total_tokens,input_tokens_cache_read/write,reasoning_tokens}}`), `samples[]` (id, epoch, input, target, messages, output, scores{value, answer, explanation}, metadata, model_usage, events transcript, error), `reductions` (K) | best-structured; read with the `inspect_ai.log` API; samples carry full agent transcripts → Run traces |
| lighteval (HF) | `results_<ts>.json` with `config_general`, `results.{"<suite>|<task>|<shots>": {metric, metric_stderr}}`, `versions`, `config_tasks`, `summary_tasks` (hashes, padded/truncated counts), details parquet per sample (K) | |
| BigCode harness | JSON `{task: {"pass@1": v, "pass@10": v}, config}` + generations JSON (K) | |
| MLflow `mlflow.evaluate` / `mlflow.genai.evaluate` | metrics logged on the run (`<metric>/v1/mean`, …), an eval-results table artifact (`eval_results_table.json`), assessments/feedback attached to traces (`mlflow.genai` scorers → Assessment{name,value,rationale,source}) (K) | mlflow trace ↔ assessment, same link as the OTel evaluation event |
| W&B Weave Evaluations | Evaluation object (dataset ref, scorers) → calls tree; per-row scorer outputs, summary per scorer (mean, true_count/fraction), model latency and token usage (K) | |
| LangSmith experiments | experiment = dataset version × target; runs with feedback{key, score, value, comment}; summary/aggregate feedback; pairwise experiments (K) | |
| Braintrust | experiment with scores{name: 0..1}, metrics (tokens, duration, cost), per-row input/output/expected/metadata; diff vs base experiment (improvements/regressions counts) (K) | regression-count UX is worth copying |
| Arize Phoenix evals | dataframes / span annotations: `label`, `score`, `explanation` per span; experiments over datasets (K) | OpenInference attrs (`llm.token_count.prompt`, `openinference.span.kind`) need mapping |
| promptfoo | `results.json`: `results.results[]` {prompt, vars, response, success, score, namedScores, gradingResult{pass,score,reason,componentResults}, latencyMs, cost}, `stats{successes,failures,tokenUsage}` (K) | red-team plugin categories → safety slices |
| DeepEval | test cases → metrics {score, threshold, success, reason, evaluation_model, evaluation_cost} (K) | |
| RAGAS | per-row metrics: faithfulness, answer_relevancy, context_precision, context_recall, context_entity_recall, noise_sensitivity, factual_correctness (K) | RAG slice |

### 4.3 LLM benchmarks by category (K)
| Category | Benchmarks | Reported metric |
|---|---|---|
| Knowledge | MMLU (57 subjects), MMLU-Pro (10 options), MMLU-Redux, TriviaQA, NaturalQuestions | acc / acc_norm (lm-eval loglikelihood), exact_match (generative) |
| Reasoning | GPQA (Diamond), BBH, ARC-Challenge/Easy, HellaSwag, WinoGrande, MuSR, DROP (F1) | acc, acc_norm, exact_match, F1 |
| Math | GSM8K, MATH / MATH-500, AIME 24/25, Minerva math | exact_match (strict / flexible filter), pass@1 avg over k samples, maj@k |
| Code | HumanEval(+), MBPP(+), LiveCodeBench, SWE-bench (Verified/Lite), BigCodeBench, Aider polyglot | pass@k (unbiased estimator), % resolved |
| Long context | RULER (by length 4k…128k), needle-in-a-haystack (depth × length grid), LongBench, InfiniteBench | acc per length bucket → curve/heatmap |
| Safety | toxicity (RealToxicityPrompts, Perspective score), refusal / over-refusal (XSTest, OR-Bench), jailbreak (HarmBench, StrongREJECT: ASR), BBQ (bias), TruthfulQA | attack success rate, refusal rate, toxicity probability |
| Instruction following / chat | IFEval (prompt/instruction-level strict/loose acc), MT-Bench (1–10 judge score), AlpacaEval 2 (LC win rate), Arena-Hard (win rate vs baseline + CI), Chatbot Arena (Elo / Bradley-Terry) | acc, judge score, win rate, LC win rate, Elo |
| Multilingual | MGSM, Global-MMLU, FLORES (chrF/BLEU), XNLI, Belebele | acc, chrF, BLEU |
| Agentic / tools | τ-bench / τ²-bench (pass^k), BFCL (AST / exec accuracy, multi-turn), GAIA, WebArena, OSWorld, Terminal-Bench | success rate, pass^k (consistency) |

### 4.4 Eval-level metrics worth showing
| Metric | Definition | View |
|---|---|---|
| Score ± CI | mean ± 1.96·stderr (or bootstrap percentile CI); always show n | Evals matrix, Run evals |
| Per-slice breakdown | score by subtask/subject/length bucket/language/difficulty | Evals matrix |
| Contamination flag | n-gram overlap of benchmark with training data / canary string found / perplexity drop; manual flag | Model version |
| Regression vs baseline | Δ with paired bootstrap over shared sample ids; McNemar for binary per-sample correctness; flag only when p<α after a multiple-comparisons correction (Holm/BH) | Run compare, Model version |
| LLM-as-judge | judge model + version, rubric hash, scores; agreement with humans (Cohen's κ / Spearman), position bias (swap-order consistency rate), length bias (score vs length correlation), self-preference | Run evals |
| Human eval | inter-rater Cohen's κ (2 raters) / Fleiss' κ / Krippendorff's α (n raters, missing data), preference win rate with ties, n raters | Run evals |
| Cost & latency | tokens in/out per eval, $ (tokens × price or GPU-hours × rate), wall time, samples/s, error/timeout count | Cost, Run evals |
| Eval-in-training | periodic checkpoint evals → score vs tokens-seen / step curves, overlaid on loss; detect saturation and emergence | Run evals, Run metrics |
| Online evals | sampled production traces scored by judges (`gen_ai.evaluation.result`), guardrail hit rate (blocked/flagged %), user feedback thumbs-up rate, task success / resolution rate, refusal rate | Inference service, Model version |

**Classic ML evals (compact)**
| Task | Metrics |
|---|---|
| Classification | accuracy, precision/recall/F1 (macro/micro/weighted), ROC-AUC, PR-AUC, log loss, calibration ECE + reliability diagram, confusion matrix |
| Regression | MAE, RMSE, MAPE, R² |
| Ranking / retrieval | NDCG@k, MRR, MAP, Recall@k, hit rate |
| ASR | WER, CER (+ RTF) |
| Vision detection / seg | mAP@[.5:.95], mAP50, IoU / mIoU |
| Generation (classic) | BLEU, ROUGE-L, chrF, BERTScore, perplexity |

---

## 5. Model registry metadata
| Field | Source | Why |
|---|---|---|
| Parameter count (total / active for MoE) | safetensors index `metadata.total_size` ÷ bytes per dtype; config (num_experts, experts_per_tok) | sizing, cost |
| Architecture | config.json `architectures`, `model_type`, hidden_size, num_layers, num_attention_heads, num_key_value_heads (GQA), vocab_size | serving-engine compatibility |
| Context length | `max_position_embeddings`, `rope_scaling` | max_model_len |
| dtype / quantization | `torch_dtype`, `quantization_config` (method: awq/gptq/fp8/compressed-tensors, bits, group_size) | GPU memory, quality caveats |
| Tokenizer | tokenizer.json hash, chat_template hash, special tokens | eval reproducibility (chat template changes scores) |
| License | model card `license`, gated flag | compliance |
| Base model / lineage | model card `base_model`, `base_model_relation` (finetune/adapter/quantized/merge) | lineage graph |
| Adapters | LoRA `adapter_config.json` (r, alpha, target_modules, base_model_name_or_path) | per-adapter traffic |
| Size on disk, shards | sum of shard sizes, shard count, file digests | pull time → Pod start |
| Packaging | OCI artifact (CNCF ModelPack spec: media types `application/vnd.cncf.model.manifest.v1+json`, config with descriptor/config/modelfs; per-layer weight/config/doc media types (K)), KServe Modelcars, HF revision sha | build once / promote by digest |
| Model card | intended use, limitations, training data, eval results (`model-index` YAML: task, dataset, metrics[type,value]), carbon | Model version |
| Safety evals | refusal/jailbreak/toxicity results, red-team summary | Model version |
| Provenance | training run id, checkpoint step, dataset versions, git sha, signing/attestation (sigstore) | release ledger |

---

## Derived metrics
- Prefix-cache hit rate = Δ`prefix_cache_hits` / Δ`prefix_cache_queries` (vLLM; token-weighted).
- Spec-decode acceptance rate = Δaccepted_tokens / Δdraft_tokens; mean accepted length = 1 + Δaccepted/Δdrafts.
- Output tokens/s = rate(`generation_tokens`); per GPU = ÷ GPUs of the pod (TP size).
- Avg batch (Triton) = Δ`nv_inference_count` / Δ`nv_inference_exec_count`; mean latency = Δ`*_duration_us` / Δ`request_success`.
- KV util (TRT-LLM) = used/max blocks.
- Goodput = requests/s with TTFT ≤ T₁ **and** TPOT ≤ T₂ **and** success. Exact goodput needs per-request data (spans). The histogram approximation takes the per-bucket fraction ≤ SLO and assumes TTFT and TPOT are independent, so label it as approximate.
- Error rate = (abort + error + admission_rejections) / total; truncation rate = finished_reason=length / total.
- Cost per 1M tokens = (GPU $/h × GPUs × Δt) / (Δ(prompt+generation tokens) / 1e6); report input-weighted and output-only variants. Cached tokens count as compute-free.
- MFU (vLLM) = rate(`estimated_flops_per_gpu_total`) / peak FLOPS of the GPU SKU (from DCGM model name).
- pass@k (unbiased) = 1 − C(n−c,k)/C(n,k); stderr of a mean = sd/√n; binary: √(p(1−p)/n).
- Paired bootstrap Δ: resample shared sample ids B=1000–10000 times; CI = percentiles of the Δ means.

## Gotchas
- **Counter vs gauge semantics**: Triton durations are cumulative counters (rate them); TRT-LLM metrics are last-value gauges per iteration; `vllm:lora_requests_info` and `cache_config_info` carry their information in labels (value = timestamp/1). Never sum them.
- `kv_cache_usage_perc` is a 0–1 fraction. SGLang `token_usage` is also a fraction.
- **ITL ≠ TPOT**: vLLM ITL is per token gap; TPOT is the per-request mean. Their p99s differ greatly.
- **Renames**: vLLM `gpu_cache_usage_perc`→`kv_cache_usage_perc`, `time_per_output_token_seconds`→`inter_token_latency_seconds`, `gpu_prefix_cache_*`→`prefix_cache_*` (K); V0 `num_requests_swapped` / cpu cache gone; GIE `inference_model_*`→`inference_objective_*`; OTel `gen_ai.system`→`gen_ai.provider.name`, `gen_ai.usage.prompt_tokens`→`input_tokens`, `gen_ai.client.token.usage`→`gen_ai.client.inference.usage.*`, prompt/completion events → `gen_ai.input/output.messages` attributes.
- **Cardinality**: never label metrics by request id, prompt or user. `model_name` can be one value per LoRA adapter (bounded by max_loras). Per-sample eval data goes to rows, not metrics.
- **Multiple engines per pod (DP)**: vLLM's `engine` label. Sum across engines for throughput; average (not sum) for KV usage.
- **Multi-host (LWS)**: only the leader serves HTTP/metrics in vLLM multi-node (K), so worker GPUs show utilisation but no engine metrics. Attribute them through the LWS group index.
- **Disaggregated serving**: TTFT spans two pools (prefill + KV transfer). Per-pool TTFT is misleading, so use the router/frontend TTFT.
- Engine TTFT excludes network and gateway time. The client `time_to_first_chunk` includes them. Show both.
- **lm-eval keys**: `"acc,none"` encodes metric and filter; `acc_norm` vs `acc` and filter (`strict-match` vs `flexible-extract`) change scores by points. Scores are not comparable across harness versions, chat templates or n-shot settings. Key every score on (task version, n_shot, template hash, harness version).
- Small-n evals (AIME: 30 problems) have huge CIs. Always show n and the CI, and use averaging over k samples.
- Judge drift: the judge model version is part of the metric identity.
- Content capture is a privacy hazard. Keep it off by default and redact at the collector.

## Sources
- vLLM metric loggers (V): https://raw.githubusercontent.com/vllm-project/vllm/main/vllm/v1/metrics/loggers.py
- vLLM spec-decode metrics (V): https://raw.githubusercontent.com/vllm-project/vllm/main/vllm/v1/spec_decode/metrics.py
- vLLM perf/MFU metrics (V): https://raw.githubusercontent.com/vllm-project/vllm/main/vllm/v1/metrics/perf.py
- vLLM tracing attributes (V): https://raw.githubusercontent.com/vllm-project/vllm/main/vllm/tracing/ (otel.py / utils.py / __init__.py)
- SGLang metrics collector (V): https://raw.githubusercontent.com/sgl-project/sglang/main/python/sglang/srt/observability/metrics_collector.py
- TGI router metrics (V): https://raw.githubusercontent.com/huggingface/text-generation-inference/main/router/src/server.rs
- Triton metrics (V): https://raw.githubusercontent.com/triton-inference-server/server/main/docs/user_guide/metrics.md
- TRT-LLM backend metrics (V): https://raw.githubusercontent.com/triton-inference-server/tensorrtllm_backend/main/README.md
- OTel GenAI moved notice (V): https://raw.githubusercontent.com/open-telemetry/semantic-conventions/main/docs/gen-ai/gen-ai-metrics.md
- OTel GenAI metrics / token metrics / client inference / spans / agent spans / events (V): https://github.com/open-telemetry/semantic-conventions-genai/tree/main/docs/gen-ai (gen-ai-metrics.md, gen-ai-token-metrics.md, client-inference.md, gen-ai-spans.md, gen-ai-agent-spans.md, gen-ai-events.md)
- GIE overview (K-level, metric names unverified; metrics guide URL 404'd): https://gateway-api-inference-extension.sigs.k8s.io/
- Everything else marked (K): lm-eval-harness, Inspect AI, HELM, MLflow, Weave, LangSmith, Braintrust, Phoenix, promptfoo, DeepEval, RAGAS, KServe, Knative, Dynamo, NIM, LWS, Ray Serve, ModelPack. Verify these before implementation.

<!-- prettier-ignore-end -->
