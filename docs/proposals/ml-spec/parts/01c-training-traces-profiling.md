# 01c — Training (all phases), traces and profiling

<!--
  prettier-ignore: prettier 3.5 reads the unquoted metric names in this file
  (node_edac_*, num_retracted_*, …) as emphasis and rewrites them, and joins
  adjacent code spans, so this file is left as researched.
-->

<!-- prettier-ignore-start -->

Scope: what a training run emits (metrics, by family and by lifecycle phase), what traces and
profiles exist for training, what MLflow/W&B capture automatically, and the proposed minimal
`ml.train.*` OpenTelemetry conventions that extend the draft in forge PR #2884
("Drill-down 4: training").

Confidence: (V) = checked against an upstream source this session (URL in Sources);
(K) = from knowledge, not re-verified this session. Web search was largely unavailable; most
W&B/Lightning/Megatron key names are (K). Treat every (K) key name as "verify against the pinned
version before writing an importer".

Key observation that shapes everything below: **no exporter knows where a step begins**. GPU,
node and scheduler metrics (DCGM, NPD, Kueue) come for free; every metric in this file except §5
(tracker system metrics) and parts of §3/§4 (NCCL/FR/profilers, which are switches) needs either
(a) an import from the user's experiment tracker (W&B, MLflow, TensorBoard event files) or
(b) the `ml.train.*` callback. The product should do (a) first — zero code — and (b) for the step
breakdown, MFU and per-rank straggler views that trackers do not carry.

---

## 1. Training metrics by family

Column "Stack names" gives the key each stack logs, where it logs one: HF = HF Trainer/Accelerate
(`log_history` / tracker callbacks), PL = PyTorch Lightning, MG = Megatron-LM/Megatron-Core
(and NeMo 2 which wraps it), DS = DeepSpeed, TT = torchtitan, CM = MosaicML Composer/llm-foundry,
LV = Levanter, MX = MaxText.

### 1.1 Loss, perplexity, optimisation health

| Name (proposed `ml.train.*`) | Stack names | Type & unit | Granularity / labels | Default on? | Why it matters | Used by view | When absent |
|---|---|---|---|---|---|---|---|
| `ml.train.loss` | HF `loss` (V, TRL DPO doc lists `loss`), `eval_loss`; PL user `self.log("train_loss")`; MG `lm loss` (K); DS user-logged; TT `loss_metrics/global_avg_loss`, `global_max_loss` (K); CM `loss/train/total` (K); LV `train/loss` (K); MX `learning/loss` (K) | gauge, 1 (nats/token) | per step; `ml.split`∈{train,val,test}; optional `ml.data.source` (bounded ≤64) | yes in every stack | primary convergence signal; divergence, plateau | Run overview, Run metrics, Run compare | `run-loss-missing`: import tracker or add callback |
| `ml.train.loss` by domain | CM/llm-foundry eval per-dataset `metrics/eval/<name>/LanguageCrossEntropy` (K); OLMo `eval/<domain>/CrossEntropyLoss` (K) | gauge | `ml.data.source` closed set from mixture config | no (opt-in eval sets) | data-mixture regressions hidden in the mean | Run metrics, Evals matrix | `loss-per-domain-missing`: add per-source val sets |
| `ml.train.perplexity` | HF none (compute exp(eval_loss)); CM `LanguagePerplexity` (K); MG `lm loss PPL` in validation print (K) | derived gauge | = exp(loss) | derived | human-readable loss | Run metrics | derived, never missing if loss present |
| `ml.train.grad.norm` | HF `grad_norm` (V, pre-clip); MG `grad-norm` (K); DS `get_global_grad_norm()` (K); TT `grad_norm` (K); CM `l2_norm/grad/global` (K); LV `train/grad_norm` (K); MX `learning/raw_grad_norm` (K) | gauge, 1 | per step; global; optional `ml.layer` (bounded by depth, opt-in) | yes HF/MG/TT/MX | spikes precede loss spikes; vanishing grads | Run metrics | `grad-norm-missing` |
| `ml.train.grad.clipped` | not standard; derive grad_norm > max_grad_norm | counter, {step} | per step | no | fraction of steps clipped (>~5% sustained = LR too high) | Run metrics | derived when clip threshold known |
| `ml.train.lr` | HF `learning_rate` (V); PL `LearningRateMonitor` → `lr-<Optimizer>` (K); MG `learning-rate` (K); TT `lr` (K); CM `lr-<Opt>/group0` (K); LV `train/learning_rate` (K); MX `learning/current_learning_rate` (K) | gauge, 1 | per step; `ml.param_group` (≤8) | yes | schedule bugs; correlate spikes with warmup end | Run metrics | `lr-missing` |
| `ml.train.lr.phase` | none (WSD schedules: MiniCPM/OLMo; derive from scheduler config) | attribute on step span/event ∈ {warmup, stable, decay, cooldown} | per step | no | explains loss shape (WSD decay drop is expected, not a win) | Run overview | infer from LR curve |
| `ml.train.weight.norm` | MG `params-norm` (`--log-params-norm`) (K); CM `l2_norm/moment`, `l2_norm/param` via OptimizerMonitor (K); MX `learning/param_norm` (K) | gauge | global, opt per layer | no (MG flag) | weight growth → instability, wd misconfig | Run metrics | — |
| `ml.train.update_ratio` | CM OptimizerMonitor `l2_norm/update/...` (K); hand-computed ‖Δθ‖/‖θ‖ | gauge, 1 | global/per layer | no | ~1e-3 healthy; drift = LR mis-scaled | Run metrics | — |
| `ml.train.loss_scale` | MG `loss-scale` (K); DS `cur_scale`/"loss scale" log line (K); HF/amp `GradScaler.get_scale()` | gauge | per step | yes when fp16 | repeated halving = overflow storm | Run metrics | n/a for bf16 |
| `ml.train.steps.skipped` | MG `number-of-skipped-iterations`, `number-of-nan-iterations` (K); DS "OVERFLOW! Skipping step" log (K) | counter, {step} | `ml.reason`∈{overflow,nan,spike_guard} | yes MG | skipped steps waste compute and hide divergence | Run events, Run metrics | `nan-guard-missing`: enable `torch.isfinite` check |
| `ml.train.nonfinite` | TT/NeMo check, `torch.autograd.detect_anomaly` (debug only) | event | per step, `ml.rank` | no | first NaN rank/step pinpoints bad batch or bad GPU (SDC) | Run events | — |
| `ml.train.loss.spike` | none standard; derive: loss > EMA + k·σ | event (derived) | per step | derived | triggers rewind/skip-data policy (OPT/PaLM practice) | Run events | derived |

### 1.2 Throughput, step time, efficiency

| Name | Stack names | Type & unit | Granularity | Default? | Why | View | Absent |
|---|---|---|---|---|---|---|---|
| `ml.train.tokens` | TRL/HF `num_tokens` (V); MG `consumed-tokens`/`consumed-samples` (K); TT `tps` (K); CM `throughput/tokens_per_sec`, `device/tokens_per_sec` (K); LV `throughput/tokens_per_second` (K); MX `perf/per_device_tokens_per_sec` (K) | counter, {token} | job (rank 0 reports global) | yes | progress vs planned tokens; tokens/s = rate() | Run overview, Run metrics | `training-uninstrumented` |
| `ml.train.samples` | HF `train_samples_per_second` (end of run, K); CM `throughput/samples_per_sec` (K) | counter, {sample} | job | yes | packing-aware throughput | Run metrics | — |
| `ml.train.step.duration` | HF `step_time` (V in TRL GRPO, includes generation); MG `elapsed time per iteration (ms)` (K); TT `time_metrics/end_to_end(s)` (K); CM `time/batch` (K); MX `perf/step_time_seconds` (K) | histogram, s | `ml.phase` closed set | partial | step breakdown is THE diagnostic | Run metrics, Run traces | `step-breakdown-missing` |
| data wait | TT `time_metrics/data_loading(s)`, `data_loading(%)` (K); PL `SimpleProfiler` `[_TrainingEpochLoop].train_dataloader_next` (K); MG `--timing-log-level` `batch-generator` timer (K) | part of step histogram, `ml.phase=data_wait` | per rank | TT yes; others opt-in | input-bound detection (>5% = fix loader) | Run metrics | `dataloader-bound` recommendation |
| forward/backward/optimizer | MG timers `forward-backward`, `optimizer`, `forward-compute`, `backward-compute` (`--timing-log-level 1/2`) (K); DS `wall_clock_breakdown: true` → `fwd`,`bwd`,`step` ms (K) | histogram, s | per rank | no | where compute goes | Run metrics | — |
| exposed comm | MG timers `all-grads-sync`, `params-all-gather`, `optimizer-*`, `pipeline` send/recv timers (K); HTA comm/comp overlap | histogram, s | per rank | no | comm not overlapped costs money | Run metrics, Run profiles | `comm-exposed-high` |
| checkpoint stall | MG `save-checkpoint` timer (K); CM `time` around checkpoint | histogram, s, `ml.phase=checkpoint` | job | no | sync ckpt blocks training | Run checkpoints | `checkpoint-sync-stall`: use async DCP |
| `ml.train.mfu` | TT `mfu(%)` (K); CM `throughput/device/mfu` (K); LV `throughput/mfu` (K); MX `perf/per_device_tflops`, MFU in log (K); MG `throughput per GPU (TFLOP/s/GPU)` with `--log-throughput` (K) | gauge, 1 (0–1) | job | TT/CM yes | ROI headline; 35–55% dense typical on H100 | Run overview, Cost | derived from tokens/s if model shape known |
| `ml.train.hfu` | rarely logged; counts recompute FLOPs | gauge, 1 | job | no | MFU vs HFU gap = activation-recompute cost | Run metrics | derived |
| `ml.train.cost_per_token` | none | derived, $/Mtok | job | derived | throughput per $ | Cost | needs node price |

### 1.3 Memory

| Name | Source | Type & unit | Granularity | Default? | Why | View | Absent |
|---|---|---|---|---|---|---|---|
| `ml.train.memory.allocated` | `torch.cuda.memory_allocated()`; TT `memory/max_active(GiB)`, `max_reserved(GiB)` (K); CM `memory/current_allocated_mem`, `peak_allocated_mem` via MemoryMonitor (K); HF none by default (`skip_memory_metrics=True`, K); MG `--log-memory-to-tensorboard` `mem-allocated-bytes` (K); PL `DeviceStatsMonitor` → `allocated_bytes.all.current` (K) | gauge, By | per rank | TT yes | headroom → bigger micro-batch | Run system/hardware | `memory-uninstrumented` (DCGM FB_USED is the fallback, but includes allocator cache) |
| `ml.train.memory.peak` | `torch.cuda.max_memory_allocated()` (reset per step via `reset_peak_memory_stats`) | gauge, By | per rank | — | OOM margin | Run system/hardware | — |
| `ml.train.memory.reserved` | `torch.cuda.memory_reserved()` | gauge, By | per rank | — | reserved−allocated = fragmentation/cache | Run system/hardware | — |
| `ml.train.memory.alloc_retries` | `torch.cuda.memory_stats()["num_alloc_retries"]`; TT logs `num_alloc_retries`, `num_ooms` (K) | counter | per rank | TT yes | >0 means cudaFree+retry stalls → slowdowns before OOM | Run system/hardware, Recommendations | `allocator-thrash`: `PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True` |
| `ml.train.oom` | `torch.OutOfMemoryError`; `num_ooms` in memory_stats | event + counter | per rank | — | attach allocator snapshot (§4.6) | Run events | — |
| activation checkpointing | config attr `ml.ac.mode`∈{none,selective,full} | resource attribute | job | — | explains HFU>MFU | Run overview | — |

### 1.4 MoE

| Name | Stack names | Type | Granularity | Default? | Why | View | Absent |
|---|---|---|---|---|---|---|---|
| `ml.train.moe.aux_loss` | TRL `aux_loss` (V); HF `router_aux_loss_coef`, `aux_loss` in output (K); MG `load_balancing_loss` (`--moe-per-layer-logging`) (K); Mixtral/DeepSeek aux-free bias | gauge | global, opt `ml.layer` | MoE yes | load imbalance → dropped tokens / slow EP | Run metrics | `moe-lb-missing` |
| `ml.train.moe.z_loss` | MG `z_loss` (`--moe-z-loss-coeff`) (K) | gauge | global | when enabled | router logit blowup | Run metrics | — |
| `ml.train.moe.tokens_dropped` | MG capacity factor drop (K) | counter, {token} | per layer (bounded) | no | quality loss from capacity overflow | Run metrics | — |
| `ml.train.moe.expert_load` | verl `rollout/moe/max_vio/layer_i`, `min_vio`, `avg_vio` (V); MG `--moe-router-*` tokens-per-expert (K) | gauge, 1 (violation = load·E − 1) | `ml.layer` × summary {max,avg}; per-expert only as histogram | no | expert utilisation skew | Run metrics | — |

### 1.5 Data pipeline

| Name | Source | Type | Granularity | Default? | Why | View | Absent |
|---|---|---|---|---|---|---|---|
| `ml.train.data.wait` | = step.duration{phase=data_wait} | histogram s | per rank | — | input bound | Run metrics | `dataloader-bound`: more workers / pin_memory / prefetch |
| `ml.train.data.queue_depth` | DataLoader has none built in; Ray Data / WebDataset / MosaicML Streaming expose own (K) | gauge, {batch} | per rank | no | 0 = starving | Run metrics | — |
| `ml.train.data.tokens` by source | llm-foundry/Streaming per-stream counts (K); OLMo mixture logging (K) | counter, {token}, `ml.data.source` | job | no | mixture drift vs planned weights | Run metrics | `mixture-unobserved` |
| `ml.train.data.mixture_weight` | config / curriculum scheduler | gauge, 1, `ml.data.source` | job | no | curriculum phases | Run overview | — |
| `ml.train.data.position` | HF `epoch` (V, float); MG `consumed-samples`; TT/DCP dataloader state; Streaming `sample_in_epoch` (K) | gauge | job | yes | exact resume, skip-data after rewind | Run checkpoints | — |

### 1.6 Distributed

| Name | Source | Type | Granularity | Default? | Why | View | Absent |
|---|---|---|---|---|---|---|---|
| parallelism degrees | env `WORLD_SIZE`, `LOCAL_WORLD_SIZE`; MG args `tensor_model_parallel_size`, `pipeline_model_parallel_size`, `context_parallel_size`, `expert_model_parallel_size`; DS `zero_optimization.stage`; TT `parallelism.*` (K) | resource attrs `ml.world_size`, `ml.parallel.dp/tp/pp/cp/ep`, `ml.zero_stage`/`ml.fsdp.strategy` | job | config | MFU interpretation; compare runs | Run overview, Run compare | infer from pod count |
| `ml.train.pipeline.bubble` | derived: (pp−1)/(m+pp−1) for 1F1B (m microbatches); interleaved ÷ v | gauge, 1 | job | derived | PP waste | Run metrics | derived |
| `ml.train.collective.duration` | Flight Recorder `duration_ms` with `TORCH_NCCL_ENABLE_TIMING=1` (V); NCCL profiler plugin; HTA | histogram, s | `ml.collective.op` ∈ {all_reduce, all_gather, reduce_scatter, all_to_all, broadcast, send, recv, barrier}, `ml.pg` (≤8 named groups: dp,tp,pp,cp,ep,…) | no | per-op comm time | Run traces, Run profiles | `nccl-timing-off` |
| `ml.train.collective.bytes` | FR `input_sizes`×dtype (V) | counter, By | op, pg | no | busbw = algbw × factor | Run metrics | — |
| algbw / busbw | nccl-tests definitions: algbw = bytes/t; busbw = algbw × 2(n−1)/n for all_reduce, (n−1)/n for all_gather/reduce_scatter/all_to_all, 1 for broadcast/send (K, nccl-tests PERFORMANCE.md) | derived GB/s | op | derived | compare to fabric peak (NVLink ~450 GB/s/dir H100; IB 400Gb/s NIC ≈ 50 GB/s) | Run metrics, Node detail | — |
| NCCL env/version | `torch.cuda.nccl.version()`; env `NCCL_*`, `TORCH_NCCL_*` | resource attrs `ml.nccl.version`, selected env snapshot event | job | — | regressions tracked to NCCL bumps | Run overview | — |
| `ml.train.rank.step.duration` | per-rank timers; MG straggler detector `--log-straggler` → prints min/max rank throughput/power (K); NVIDIA Resiliency Ext `straggler` module: per-rank `relative perf score`, `individual perf score` (K) | histogram, s, `ml.rank` | per rank (bounded by world size; sample above 1024) | no | slow rank = bad GPU/NIC/thermal | Run system/hardware | `straggler-undetected` |

### 1.7 Checkpointing

| Name | Source | Type | Granularity | Default? | Why | View | Absent |
|---|---|---|---|---|---|---|---|
| `ml.checkpoint.duration` | MG `save-checkpoint` timer (K); DCP `torch.distributed.checkpoint.async_save` futures (K); CM checkpoint logs | histogram, s, `ml.checkpoint.op`∈{save_blocking, save_background, upload, load} | job | partial | stall vs background time | Run checkpoints | `checkpoint-unobserved` |
| `ml.checkpoint.bytes` | sum of shard files | counter, By | per checkpoint | — | storage cost; upload time | Run checkpoints | — |
| `ml.checkpoint.step` / last good step | latest dir / `latest_checkpointed_iteration.txt` (MG, K) | gauge | job | — | steps lost on restart = step − last_good | Run checkpoints, Run events | — |
| `ml.checkpoint.interval` | config | attr | job | — | Young/Daly optimum √(2·save_cost·MTBF) | Recommendations | `checkpoint-interval-suboptimal` |
| format | attr `ml.checkpoint.format` ∈ {dcp, megatron_dist, deepspeed, safetensors, orbax} | attr | — | — | sharded vs consolidated | Run checkpoints | — |

### 1.8 Fault tolerance

| Name | Source | Type | Default? | Why | View | Absent |
|---|---|---|---|---|---|---|
| restarts | torchelastic `TORCHELASTIC_RESTART_COUNT`, `--max-restarts` (K); JobSet `restartAttempt` / Kubeflow Trainer status; NVIDIA Resiliency Ext (`nvidia-resiliency-ext`) in-job restart / in-process restart (K) | event + `ml.attempt` resource attr | yes (env) | attempts/waves | Run events | — |
| hang | NVRx `fault_tolerance` heartbeat/section timeouts (K); NCCL watchdog `TORCH_NCCL_HEARTBEAT_TIMEOUT_SEC` (default 480s, K); `TORCH_NCCL_ASYNC_ERROR_HANDLING` (K); PG `timeout` (default 10 min NCCL, K) | event | partial | hang ≠ crash; FR dump attach | Run events, Run traces | `flight-recorder-off` |
| watchdog timeouts | log line "Watchdog caught collective operation timeout: WorkNCCL(SeqNum=…, OpType=ALLREDUCE…)" (K) | event parsed from logs | yes (logged) | identifies op + seq | Run events | — |

---

## 2. Lifecycle phases and attempts

Model: a **Run** has an ordered list of **phases** (pretrain, midtrain, sft, preference, rl,
distill, qat, eval) and each phase has **attempts** (waves). `ml.phase.kind` (resource) and
`ml.attempt` (resource, increments on restart/resume/resize/rewind) partition every series. Don't
confuse with the step-breakdown `ml.phase` attribute — **propose renaming the step attribute to
`ml.step.phase`** to avoid the collision.

| Phase | Distinctive metrics (stack keys) | Notes |
|---|---|---|
| Pre-training | tokens seen vs planned (`ml.train.tokens` / `ml.train.tokens.planned`), loss per domain, mixture weights, scaling-law fit (loss vs compute: L(C)=E+A·C^−α, Chinchilla-style), MFU | weeks long; rewind events matter |
| Continued pre-training / mid-training | same + loss on original-domain held-out (forgetting), annealing LR phase, mixture shift | WSD decay phase often = mid-training |
| SFT | per-task loss, `mean_token_accuracy` (TRL SFT, K; DPO doc lists it V), packing efficiency = non-pad tokens / total tokens, `num_tokens` (V) | loss only on completion tokens |
| DPO/IPO/KTO (offline preference) | TRL: `rewards/chosen`, `rewards/rejected`, `rewards/margins`, `rewards/accuracies`, `logps/chosen`, `logps/rejected`, `logits/chosen`, `logits/rejected`, `entropy`, `grad_norm`, `learning_rate` (V); KTO adds `kl` and per-class `rewards/chosen_sum`-style keys (K) | accuracies→1.0 fast with margins exploding = overfit; chosen logps falling = likelihood displacement |
| GRPO (online RL) | TRL: `reward`, `reward_std`, `rewards/<fn>/mean|std`, `frac_reward_zero_std`, `kl` (only if beta≠0; default beta=0), `entropy`, `clip_ratio/region_mean`, `clip_ratio/low_mean`, `clip_ratio/high_mean`, `completions/mean_length`, `min_length`, `max_length`, `mean_terminated_length`, `completions/clipped_ratio`, `step_time`, `num_tokens`, `sampling/sampling_logp_difference/mean|max`, `sampling/importance_sampling_ratio/min|mean|max`, `tools/call_frequency`, `tools/failure_frequency` (V) | entropy collapse; length explosion; train–inference logprob mismatch (vLLM vs trainer) is a first-class health signal |
| PPO / RLHF | verl: `critic/score/mean|max|min`, `critic/rewards/mean`, `critic/advantages/mean`, `critic/returns/mean`, `critic/values/mean`, `critic/vf_loss`, `critic/vf_clipfrac`, `actor/pg_loss`, `actor/pg_clipfrac`, `actor/ppo_kl`, `actor/entropy`, `actor/grad_norm`, `response_length/mean|max|clip_ratio`, `prompt_length/mean`, `timing_s/gen`, `timing_s/update_actor`, `timing_s/ref`, `timing_s/values`, `timing_per_token_ms/*`, `perf/throughput`, `perf/mfu/actor`, `perf/max_memory_allocated_gb` (K, from verl metric_utils; file fetched V but the key list beyond MoE was truncated); verl rollout MoE: `rollout/moe/max_vio/*` (V). OpenRLHF: `policy_loss`, `kl`, `reward`, `return`, `response_length`, `critic_loss`, `values` (K). NeMo-Aligner: `train/rewards`, `train/init_policy_kl`, `train/ppo_ratio_clamped` (K) | generation vs training time split (`timing_s/gen` share) is the RL throughput lever |
| Reward model training | accuracy on pairs, margin, calibration | — |
| Verifier / RLVR | verifier pass rate (= mean of 0/1 reward fn), `frac_reward_zero_std` | — |
| Distillation | teacher–student KL (forward/reverse/JSD; TRL GKD logs `loss` of generalized JSD, K), student CE, teacher logits cache hit | — |
| QAT / quantisation | fake-quant loss delta vs bf16 baseline, scale/zero-point ranges, post-quant eval delta | mostly evals |
| In-training evals | HF `eval_loss`, `eval_runtime`, `eval_samples_per_second` (K); llm-foundry ICL `metrics/icl/<task>/InContextLearningAccuracy` (K); lm-eval-harness callbacks | link to Evals matrix by step |

### Attempts / waves semantics

| Event | Detect | Effect on series | Event fields |
|---|---|---|---|
| restart (crash) | pod restart, `TORCHELASTIC_RESTART_COUNT`, JobSet restartAttempt | new `ml.attempt`; step resumes from last checkpoint → step axis overlaps | from_step, to_step (resume ckpt), steps_lost, reason |
| resume from checkpoint (manual) | new job with same `ml.run.id` + load event | new attempt | checkpoint id |
| preemption | Kueue eviction / node drain / spot reclaim | attempt ends `reason=preempted` | — |
| elastic resize | world size change between attempts | `ml.world_size` changes; MFU/throughput comparable only per-GPU | old/new world size |
| rewind after loss spike | load of an earlier checkpoint + data skip (PaLM/OPT practice) | attempt with `reason=rewind`; skipped data range recorded | rewind_to_step, skipped_samples |
| config change mid-run | LR/batch change on resume | attempt attr diff | diff |

UI rule: plot by `step` with attempts overlaid (later attempt wins on overlap, earlier shown
faint); goodput = useful steps' time / wall time (Google "ML goodput"), badput categories:
scheduling, init, checkpoint load, lost steps, hang, data stall.

---

## 3. Traces

There is **no OTel semantic convention for training** (K; OTel GenAI semconv covers inference
calls only). What exists:

| Mechanism | What it gives | Default | Overhead | Notes |
|---|---|---|---|---|
| NVTX ranges (`torch.cuda.nvtx.range_push`, `nvtx` pkg) | named CPU ranges visible to Nsight Systems; MG `--nvtx-ranges`/`--profile` (K) | off unless profiler attached | ~ns when no tool attached | zero-cost markers; reuse names as span names |
| `torch.profiler.record_function` | ranges in Kineto trace | no | small, only while profiling | — |
| torch.profiler → Chrome trace JSON | full CPU+CUDA timeline | no | 5–30% while active | see §4.1 |
| OTel Python SDK | real spans to OTLP | no | µs per span | the only thing that reaches our ClickHouse traces without a converter |

Spans worth emitting (proposed): `ml.train.step` (root, one per step) with children
`ml.train.data_fetch`, `ml.train.forward`, `ml.train.backward`, `ml.train.optimizer`,
`ml.train.collective` (only exposed waits, attr op/pg/bytes), `ml.checkpoint.save`/`.load`/
`.upload`, `ml.eval` (periodic), `ml.model.load`, `ml.train.init` (process start → first step:
imports, NCCL init, ckpt load, compile — the "time to first step" badput). RL adds
`ml.rl.rollout` (generation), `ml.rl.reward`, `ml.rl.ref_logprob`, `ml.rl.update`,
`ml.rl.weight_sync` (trainer→vLLM).

Durations must come from CUDA events read one step late (PR #2884 is right); spans are back-dated.

Sampling: steps are uniform, so head-sample by step number: every step for first 50, then
1-in-N (N so ≤1 span tree/10 s per run), plus always-keep "tail" for steps whose duration
> p99 or any anomaly event; only rank 0 + the slowest rank emit child spans (span count bounded
by 2 × phases per sampled step). Checkpoint/eval/init spans always kept.

### 3.1 NCCL tracing

| Mechanism | Version | What | Notes |
|---|---|---|---|
| `NCCL_DEBUG=INFO|TRACE`, `NCCL_DEBUG_SUBSYS=INIT,NET,COLL,…`, `NCCL_DEBUG_FILE` | all | init topology, rings/trees, transport (NVLS, IB, Socket fallback!) | INFO at init is cheap & very useful: parse "Using network IB/Socket", "NCCL version" (K) |
| Profiler plugin API (`NCCL_PROFILER_PLUGIN=lib…so`) | v1 2.21?; v2 2.23; v3 2.25; v4 2.26; v5 2.28 (K — ext-profiler README not fetchable) | events: Group, Coll, P2p, ProxyOp, ProxyStep, ProxyCtrl, KernelCh, NetPlugin; v4 adds kernel channel timestamps from GPU; v5 adds CE/ stream-level | reference "inspector" plugin emits per-collective JSON (algbw/busbw, exec time) usable as metrics (K) |
| NCCL RAS | 2.24+ | `ncclras` CLI / port to query status of all ranks: missing ranks, collective progress mismatch | on by default in 2.24+ (K); good hang triage without restart |
| NCCL inspector | NCCL 2.27+ (K) | per-collective performance logs, Prometheus output mode in later versions (K) | candidate default-on low-overhead NCCL metric source |

### 3.2 PyTorch Flight Recorder (V unless noted)

- Enable: `TORCH_NCCL_TRACE_BUFFER_SIZE=N` (default 2000 per tutorial; entries in ring buffer),
  `TORCH_NCCL_DUMP_ON_TIMEOUT=true` (default false), `TORCH_FR_DUMP_TEMP_FILE` (default
  `/tmp/nccl_trace_rank_`; older name `TORCH_NCCL_DEBUG_INFO_TEMP_FILE`). Optional
  `TORCH_NCCL_TRACE_CPP_STACK`, `TORCH_NCCL_ENABLE_TIMING` (adds `duration`), `TORCH_SYMBOLIZE_MODE`.
- On demand: `torch._C._distributed_c10d._dump_nccl_trace(includeCollectives, includeStackTraces, onlyActive)` → pickle; custom `DebugInfoWriter` to write elsewhere (C++).
- Dump fields: `version`, `pg_config{name,desc,ranks}`, `pg_status{last_enqueued_collective,last_started_collective,last_completed_collective}`, `entries[]{frames[{name,filename,line}], record_id, pg_id, process_group, collective_seq_id, p2p_seq_id, op_id, profiling_name (e.g. nccl:all_reduce), time_created_ns, input_sizes, input_dtypes, output_sizes, output_dtypes, state (scheduled|started|completed), time_discovered_started_ns, time_discovered_completed_ns, retired, timeout_ms, is_p2p}` (+`duration_ms` when timing on).
- Analyzer: `torchfrtrace <dir> [--prefix] [-j] [--selected-ranks] [--pg-filters]` → "Not all ranks joining collective N … missing ranks: {1}".
- Product: one pickle per rank lands on local disk of the pod → **must be on a volume we collect** (emptyDir is lost when the pod dies). Recommendation `flight-recorder-off`: set the three envs + mount `TORCH_FR_DUMP_TEMP_FILE` on a PVC/hostPath; a sidecar/agent ships files and runs the analyzer server-side, attaching "missing ranks / mismatched op" to the hang event.
- Overhead: buffer record per collective, negligible (K); timing adds CUDA events.

### 3.3 Hang detection (layered)

1. Progress stall: `ml.train.step` unchanged for > k × p50 step time (we can do this from imported tracker metrics too). 2. GPU signature: DCGM SM activity ≈ 0 but `DCGM_FI_PROF_NVLINK/PCIE` low, power at idle — or 100% "util" with tensor activity 0 (spinning NCCL kernel). 3. NCCL watchdog timeout logs / FR dump. 4. NVRx heartbeat. 5. py-spy dump of all ranks (§4.7) on detection.

---

## 4. Profiling

| Tool | Captures | Trigger | Overhead | Artifact / size | k8s on-demand |
|---|---|---|---|---|---|
| 4.1 PyTorch profiler (Kineto/CUPTI) | activities CPU, CUDA (XPU…); `schedule(wait, warmup, active, repeat, skip_first)`; `record_shapes`, `profile_memory`, `with_stack`, `with_flops`, `with_modules`; outputs `export_chrome_trace` JSON, `tensorboard_trace_handler`, `export_memory_timeline` HTML, `export_stacks`, `key_averages()` (K) | in code; TT `--profiling.enable_profiling --profiling.profile_freq` (K); MG `--profile --profile-step-start/end --use-pytorch-profiler` (K); HF via callback; PL `profiler="pytorch"` | 5–30% active, ~0 in wait | JSON 50 MB–2 GB per rank per few steps (gz ~10×) | **dynolog + Kineto on-demand** (`KINETO_USE_DAEMON=1`, `dyno gputrace --pids … --duration-ms`) — no code change, the right primitive (K); else file-trigger polled by callback; `SIGUSR` handler |
| HTA (Holistic Trace Analysis) | over per-rank Kineto traces: temporal breakdown (compute/non-compute/idle), idle time breakdown (host wait, kernel wait, other), kernel breakdown (comm/compute/memory), comm–comp overlap %, CUDA kernel launch stats (short kernels, launch delay), queue length, frequent CUDA kernel sequences, trace diff (K) | offline | n/a | tables | server-side analysis of uploaded traces → Run profiles |
| 4.2 Nsight Systems | CUDA API, kernels, memcpy, NVTX, OS runtime (pthread, syscalls), cuBLAS/cuDNN, NCCL (NVTX in NCCL), CPU sampling, GPU metrics sampling (`--gpu-metrics-device`, SM/DRAM/NVLink at kHz), IB/NIC counters (`--nic-metrics`) (K) | `nsys profile --capture-range=cudaProfilerApi|nvtx`, `nsys launch` + `nsys start/stop`; needs binary in image or Nsight operator injector (NVIDIA "Nsight Systems k8s injector"/devtools sidecar admission webhook) (K) | 5–20%+; GPU metrics sampling needs perf counters | `.nsys-rep` 100 MB–several GB/rank for ~1 min; export sqlite | `nsys` via `kubectl exec` if installed; injector webhook |
| 4.3 Nsight Compute | per-kernel: SOL% (compute/memory), roofline, occupancy, warp stall reasons, memory chart (K) | `ncu --kernel-name --launch-skip --launch-count` | 10–1000× per profiled kernel (replays) | `.ncu-rep` | not for production runs; dev only |
| 4.4 DCGM profiling fields (`DCGM_FI_PROF_*`) | SM active/occupancy, tensor active, DRAM active, NVLink/PCIe bytes, ~1 Hz | dcgm-exporter | <1% | metrics | default-on in our fleet view |
| **CUPTI/perf-counter conflict** | DCGM profiling metrics and Nsight Systems GPU-metrics / Nsight Compute / CUPTI range profiling contend for the same HW counters; only one at a time per GPU. Fix: `dcgmi profile --pause` / dcgm-exporter pause before profiling, `--resume` after (K) | — | — | — | the on-demand profiler must pause DCGM profiling on the target GPUs, and Fleet must show a "profiling in progress" gap, not "0% tensor active" |
| 4.5 Kineto activity only (no counters) | torch.profiler CUDA activity uses CUPTI activity API, which **coexists** with DCGM (K); only `experimental_config` with CUPTI metrics conflicts | — | — | — | — |
| 4.6 Memory snapshot | `torch.cuda.memory._record_memory_history(max_entries=100000)`; `torch.cuda.memory._dump_snapshot("x.pickle")`; viewer pytorch.org/memory_viz (K) | in code, or on OOM handler | moderate (stack capture per alloc) | pickle 10–500 MB | arm on demand; auto-dump on `OutOfMemoryError` → Run events attachment |
| 4.7 py-spy / austin | Python stacks of a live PID: `py-spy dump --pid` (instant all-thread stacks, `--native`), `py-spy record` flamegraph/speedscope (K) | attach | ~0 for dump; low sampling | text / svg | needs `SYS_PTRACE` cap in pod (or shareProcessNamespace sidecar); best hang tool |
| 4.8 eBPF continuous CPU | OTel eBPF profiler (ex-Elastic, donated 2024) — unwinds CPython, native, kernel, no instrumentation; pprof → OTLP profiles (K); Parca agent, Grafana Pyroscope/Alloy (`pyroscope.ebpf`) same idea | always on (DaemonSet, privileged) | ~1% CPU | OTLP profiles | fits our existing continuous profiling store; shows dataloader worker CPU, tokenizer, GIL |
| 4.9 perf | `perf record -g`, `perf top` with Python 3.12 `-X perf` trampolines | attach | low | perf.data | node debug only |
| 4.10 JAX | `jax.profiler.start_trace/stop_trace`, `start_server(port)` for on-demand capture via XProf/TensorBoard profile plugin; output `.xplane.pb` (K); MaxText `profiler=xplane`, `skip_first_n_steps_for_profiler`, `profiler_steps` (K) | port / code | similar to Kineto | xplane 100s MB | port-forward capture |

On-demand vs continuous: continuous = DCGM fields, eBPF CPU, `ml.train.*` metrics, NCCL inspector
counters, Flight Recorder ring (all ≤1%). On-demand = Kineto (via dynolog), nsys, memory snapshot,
py-spy dump. Triggers: (1) dynolog RPC (best), (2) file sentinel on a shared volume polled per
step by the callback (works with any stack), (3) signal (`SIGUSR1/2`) handled by the callback,
(4) env + restart (worst). Always profile a bounded window (e.g. 5 active steps) on ≤N ranks (rank 0
+ the straggler).

---

## 5. What experiment trackers capture automatically (import targets)

### W&B (`wandb` system monitor, default every ~15 s; keys in history under `system/` prefix in the UI) (K — source not fetchable this session)

| Key | Unit |
|---|---|
| `system/gpu.{i}.gpu` | % utilisation |
| `system/gpu.{i}.memory` | % memory-controller util |
| `system/gpu.{i}.memoryAllocated` | % of total FB |
| `system/gpu.{i}.memoryAllocatedBytes` | bytes |
| `system/gpu.{i}.temp` | °C |
| `system/gpu.{i}.powerWatts`, `system/gpu.{i}.powerPercent`, `enforcedPowerLimitWatts` | W, % |
| `system/gpu.{i}.smClock`, `memoryClock`, `graphicsClock` | MHz |
| `system/gpu.{i}.correctedMemoryErrors`, `uncorrectedMemoryErrors` | count |
| `system/gpu.process.{i}.*` | same, for GPUs used by the process |
| `system/cpu`, `system/cpu.{i}.cpu_percent`, `system/proc.cpu.threads` | % |
| `system/memory`, `system/proc.memory.rssMB`, `availableMB`, `percent` | % / MB |
| `system/disk.{path}.usagePercent`, `disk.in`, `disk.out` | % / MB |
| `system/network.sent`, `network.recv` | bytes |
| (newer) `system/tpu.*`, `system/trn.*`, AMD `gpu.{i}.*` via rocm-smi | — |

### MLflow (`mlflow.enable_system_metrics_logging()` / `MLFLOW_ENABLE_SYSTEM_METRICS_LOGGING=true`; off by default; sample 10 s, aggregate & log every 6 samples; logged under `system/` prefix) (V for GPU keys)

| Key | Unit |
|---|---|
| `system/gpu_{i}_memory_usage_percentage`, `system/gpu_{i}_memory_usage_megabytes` | %, MB |
| `system/gpu_{i}_utilization_percentage` | % |
| `system/gpu_{i}_power_usage_watts`, `system/gpu_{i}_power_usage_percentage` | W, % of enforced limit |
| `system/cpu_utilization_percentage`, `system/system_memory_usage_megabytes`, `system/system_memory_usage_percentage`, `system/disk_usage_megabytes`, `system/disk_usage_percentage`, `system/disk_available_megabytes`, `system/network_receive_megabytes`, `system/network_transmit_megabytes` (K) | — |

Import guidance: these are per-process NVML samples with no pod/node identity beyond the host
name; join them to DCGM by (hostname, GPU index → UUID via run's `CUDA_VISIBLE_DEVICES`). Prefer
DCGM when both exist; use tracker system metrics only as the fallback when DCGM is absent
(`dcgm-exporter-missing`). Also import tracker **config** (hyperparameters → run attrs), **summary**
(final metrics), and **artifacts** (checkpoints → Run checkpoints).

TensorBoard event files (MG, NeMo, LV, MX, PL default logger) are the other import target: tags
as listed in §1, `scalars` only.

---

## 6. Proposed `ml.train.*` conventions (extends PR #2884)

Resource attributes (every series): `ml.run.id`, `ml.training.name`, `ml.attempt` (int),
`ml.phase.kind` ∈ {pretrain, midtrain, sft, preference, rl, distill, qat, eval, other},
`ml.world_size`, `ml.parallel.dp|tp|pp|cp|ep` (ints), `ml.framework` ∈ {hf, lightning, megatron,
nemo, deepspeed, torchtitan, composer, levanter, maxtext, verl, trl, openrlhf, other},
`ml.framework.version`, `ml.nccl.version`, `ml.model.params` (int), `ml.model.active_params`
(MoE), `ml.rank` **only** on per-rank instruments (not resource — keeps cardinality explicit).

Global attribute closed sets: `ml.step.phase` ∈ {data_wait, forward, backward, optimizer,
comm_exposed, checkpoint, eval, other} (renamed from draft's `ml.phase`, kept as deprecated alias);
`ml.split` ∈ {train, val, test}; `ml.collective.op` (8 values); `ml.pg` ≤ 8; `ml.data.source` ≤ 64
(declared up front; overflow → `other`); `ml.layer` ≤ 256 and opt-in only.

| Instrument | Kind | Unit | Attributes | Cardinality bound | Emitter | Status |
|---|---|---|---|---|---|---|
| `ml.train.step` | gauge | {step} | — | 1 | rank 0 | draft |
| `ml.train.steps.total` | gauge | {step} | — | 1 | rank 0 | draft |
| `ml.train.tokens` | counter | {token} | `ml.data.source`? | ≤64 | rank 0 (global) | draft (+source) |
| `ml.train.tokens.planned` | gauge | {token} | — | 1 | rank 0 | new |
| `ml.train.samples` | counter | {sample} | — | 1 | rank 0 | draft |
| `ml.train.step.duration` | histogram | s | `ml.step.phase` | 8 | rank 0 | draft (renamed attr) |
| `ml.train.rank.step.duration` | histogram | s | `ml.rank`, `ml.step.phase`∈{total, comm_exposed, data_wait} | 3 × world (cap: emit only top/bottom-k ranks above 1024) | every rank | draft (+data_wait) |
| `ml.train.loss` | gauge | 1 | `ml.split`, `ml.data.source`? | 3 × 64 | rank 0 | new |
| `ml.train.grad.norm` | gauge | 1 | `ml.layer`? | 1 (256 opt-in) | rank 0 | new |
| `ml.train.grad.clipped` | counter | {step} | — | 1 | rank 0 | new |
| `ml.train.lr` | gauge | 1 | `ml.param_group`? | ≤8 | rank 0 | new |
| `ml.train.weight.norm` | gauge | 1 | `ml.layer`? | 1 | rank 0 | new, opt-in |
| `ml.train.update_ratio` | gauge | 1 | `ml.layer`? | 1 | rank 0 | new, opt-in |
| `ml.train.loss_scale` | gauge | 1 | — | 1 | rank 0 | new (fp16 only) |
| `ml.train.steps.skipped` | counter | {step} | `ml.reason`∈{overflow, nonfinite, spike_guard} | 3 | rank 0 | new |
| `ml.train.mfu` | gauge | 1 | — | 1 | rank 0 (or server-derived) | new |
| `ml.train.flops` | counter | {flop} | — (model FLOPs, not hardware) | 1 | rank 0 | new; enables server-side MFU |
| `ml.train.memory.allocated` | gauge | By | `ml.rank`, `ml.stat`∈{current, peak, reserved} | 3 × world | every rank | new |
| `ml.train.memory.alloc_retries` | counter | {retry} | `ml.rank` | world | every rank | new |
| `ml.train.oom` | counter + event | {event} | `ml.rank` | world | every rank | new |
| `ml.train.moe.aux_loss` | gauge | 1 | `ml.kind`∈{load_balance, z} | 2 | rank 0 | new |
| `ml.train.moe.tokens_dropped` | counter | {token} | — | 1 | rank 0 | new |
| `ml.train.moe.expert_load_violation` | gauge | 1 | `ml.stat`∈{max, avg}, `ml.layer`? | 2 (×256 opt-in) | rank 0 | new |
| `ml.train.data.wait` | — | — | use step.duration{data_wait} | — | — | (no new instrument) |
| `ml.train.data.queue_depth` | gauge | {batch} | `ml.rank` | world | every rank | new, optional |
| `ml.train.data.mixture_weight` | gauge | 1 | `ml.data.source` | 64 | rank 0 | new |
| `ml.train.collective.duration` | histogram | s | `ml.collective.op`, `ml.pg` | 64 | rank 0 + sampled ranks | new |
| `ml.train.collective.bytes` | counter | By | `ml.collective.op`, `ml.pg` | 64 | same | new |
| `ml.checkpoint.duration` | histogram | s | `ml.checkpoint.op`∈{save_blocking, save_background, upload, load} | 4 | rank 0 | draft (+op) |
| `ml.checkpoint.bytes` | counter | By | — | 1 | rank 0 | draft |
| `ml.checkpoint.step` | gauge | {step} | — (last durable checkpoint) | 1 | rank 0 | draft (was an attribute; a gauge avoids unbounded cardinality) |
| `ml.train.restarts` | counter | {restart} | `ml.reason`∈{crash, preempted, hang, rewind, resize, manual} | 6 | controller/rank 0 | new |
| `ml.train.goodput` | gauge (derived server-side) | 1 | — | 1 | server | new |
| RL: `ml.rl.reward` | gauge | 1 | `ml.stat`∈{mean, std}, `ml.reward.fn`≤16 | 32 | rank 0 | new |
| RL: `ml.rl.kl` | gauge | 1 | — | 1 | rank 0 | new |
| RL: `ml.rl.entropy` | gauge | 1 | — | 1 | rank 0 | new |
| RL: `ml.rl.clip_fraction` | gauge | 1 | `ml.bound`∈{low, high, region} | 3 | rank 0 | new |
| RL: `ml.rl.response.length` | histogram | {token} | `ml.terminated`∈{true,false} | 2 | rank 0 | new |
| RL: `ml.rl.rollout.tokens` | counter | {token} | — | 1 | rank 0 | new |
| RL: `ml.rl.logprob_mismatch` | gauge | 1 | `ml.stat`∈{mean, max} | 2 | rank 0 | new |
| Pref: `ml.pref.reward_margin`, `ml.pref.accuracy`, `ml.pref.logp` | gauge | 1 | `ml.choice`∈{chosen, rejected} on logp | ≤2 | rank 0 | new |
| Distill: `ml.distill.kl` | gauge | 1 | — | 1 | rank 0 | new |
| `ml.eval.score` | gauge | 1 | `ml.eval.task` ≤ 128, `ml.eval.metric` ≤ 8 | 1024 | eval | new (owned by evals slice) |
| Spans | `ml.train.step` (+children per §3), `ml.train.init`, `ml.checkpoint.*`, `ml.eval`, `ml.rl.*`, `ml.model.load` | — | sampled per §3 | ≤2 trees/10 s/run | — | draft `train.step` renamed `ml.train.step` |
| Events (log records, `event.name`) | `ml.train.nonfinite`, `ml.train.loss_spike`, `ml.train.oom` (+snapshot artifact link), `ml.train.hang` (+FR analysis), `ml.train.attempt.start/end`, `ml.checkpoint.saved`, `ml.profile.captured` (artifact URI) | — | — | — | — | new |

Rule for anything a tracker already logs: **import, don't duplicate** — the callback maps HF/TRL
keys (`loss`, `grad_norm`, `learning_rate`, `num_tokens`, `reward`, `kl`, `rewards/margins`, …) to
these names; arbitrary other user keys go to `ml.train.custom` with `ml.metric.name` bounded at 256
per run.

---

## Derived metrics

- tokens/s (global) = rate(`ml.train.tokens`); per GPU = ÷ `ml.world_size`.
- MFU = (model FLOPs per token × tokens/s) / (n_gpus × peak dense FLOP/s at the training dtype).
  Dense decoder FLOPs/token ≈ 6N + 12·L·d·s (training, fwd+bwd; attention term: L layers, d
  hidden, s seq len; ×½ for causal if the kernel skips masked blocks — state which). MoE: use
  N_active (shared + top-k experts + attention + embedding-out) not N_total. HFU adds recompute:
  full AC ≈ 8N instead of 6N. Peak: H100 SXM bf16 dense 989 TFLOP/s, fp8 1979; A100 bf16 312 (K).
- goodput = Σ time of steps that survive into the final model / wall time since first pod Running.
- steps lost on restart = step at failure − `ml.checkpoint.step`.
- straggler score per rank = rank p50 step time / median over ranks (>1.05 sustained = flag);
  NVRx uses relative perf score similarly (K).
- pipeline bubble = (pp−1)/(m+pp−1) (1F1B); interleaved: (pp−1)/(v·m+pp−1).
- busbw = algbw × {2(n−1)/n all_reduce; (n−1)/n all_gather, reduce_scatter, all_to_all; 1 bcast, p2p}.
- fragmentation = reserved − allocated (bytes); ratio = 1 − allocated/reserved.
- packing efficiency = non-pad tokens / (batch × seq len).
- RL gen share = `timing_s/gen` / step time.
- cost per Mtok = (node $/h × nodes) / (tokens/h / 1e6).
- optimal checkpoint interval (Young) = √(2 × save_blocking_s × MTBF_s).

## Gotchas

- **CPU timers lie** about GPU work (async kernels); use CUDA events read a step late; `torch.cuda.synchronize()` in a logging callback silently slows training (HF `skip_memory_metrics` exists for this reason).
- **Rank-0-only logging**: HF/Lightning log rank-0 values; loss is often local micro-batch loss, not the all-reduced global — label `ml.loss.reduction` ∈ {local, global_mean}.
- **Gradient accumulation**: "step" = optimizer step, not micro-batch; HF `global_step` vs micro steps; tokens counts must be per optimizer step.
- **Step axis restarts / overlaps** across attempts; never sum counters across attempts naïvely (counter resets on restart — use OTel cumulative with start time; ClickHouse queries must handle resets).
- **Tracker metrics are last-value or interval mean**: HF logs averages over `logging_steps`; MLflow system metrics are 1-min means of 10-s samples; W&B ~15 s. Don't compute percentiles from them.
- **NVML "utilization" ≠ busy**: 100% means a kernel was resident; a spinning NCCL kernel during a hang shows 100%. Use DCGM PROF SM/tensor active.
- **Perf-counter contention**: DCGM profiling vs nsys GPU metrics/ncu — pause DCGM, and mark the gap.
- **Per-rank cardinality**: 16k-GPU jobs → per-rank series must be capped (top/bottom-k + histogram of the rest); per-layer and per-expert metrics opt-in only.
- **`ml.phase` collision** between lifecycle phase and step phase in the draft — rename.
- **Flight Recorder files die with the pod** unless written to a collected volume; default `/tmp`.
- **Profiler artifacts are big** (GBs/rank); upload compressed to object storage, keep a trace index in ClickHouse, not the trace itself.
- **py-spy needs SYS_PTRACE**; many clusters' PSA "restricted" forbids it — fall back to the in-process faulthandler (`faulthandler.dump_traceback_later`/`SIGUSR1` registration).
- **MIG/time-slicing**: tracker per-process GPU metrics on MIG report the parent GPU or fail (NVML util unsupported on MIG) — use DCGM MIG entity metrics.
- **TRL GRPO `kl` is absent by default** (beta=0), not zero — absence ≠ healthy.
- **Version renames**: TRL moved `completion_length`→`completions/mean_length`, `clip_ratio`→`clip_ratio/region_mean` (K); MLflow system metric names stable since 2.8; torchtitan metric names changed with its metrics refactor (K) — the importer needs per-version mapping tables.

## Sources

(V) verified this session:
- TRL GRPO logged metrics: https://raw.githubusercontent.com/huggingface/trl/main/docs/source/grpo_trainer.md
- TRL DPO logged metrics: https://raw.githubusercontent.com/huggingface/trl/main/docs/source/dpo_trainer.md
- MLflow GPU monitor keys: https://raw.githubusercontent.com/mlflow/mlflow/master/mlflow/system_metrics/metrics/gpu_monitor.py
- PyTorch Flight Recorder (envs, dump format, analyzer): https://raw.githubusercontent.com/pytorch/tutorials/main/unstable_source/flight_recorder_tutorial.rst
- verl rollout MoE load-balance metrics: https://raw.githubusercontent.com/volcengine/verl/main/verl/trainer/ppo/metric_utils.py
- Draft conventions: forge PR #2884 diff ("Drill-down 4", now in
  [`docs/proposals/ml-workloads.md`](../../ml-workloads.md#drill-down-4-training-and-where-a-steps-time-goes))

(K) to verify before implementation (attempted, not reachable this session): W&B system metrics
(docs.wandb.ai access-restricted; wandb `gpu_stats` source path moved), NCCL ext-profiler README
(https://github.com/NVIDIA/nccl/tree/master/ext-profiler), PyTorch `TORCH_NCCL_*` env doc,
Megatron `training.py` log keys, torchtitan `metrics.py`, Composer callbacks (SpeedMonitor,
MemoryMonitor, OptimizerMonitor), MaxText `metric_logger`, HTA docs
(https://hta.readthedocs.io), nvidia-resiliency-ext docs, dynolog README, nccl-tests PERFORMANCE.md.

<!-- prettier-ignore-end -->
