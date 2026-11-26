// Copyright (c) 2026 Reliant Labs

/**
 * The ML world's numbers that more than one domain shows, as pure functions
 * of (phase, step) or (task, subject), so a checkpoint's val loss in the
 * Checkpoints table, the point on the loss chart at that step, and the eval
 * matrix's cell for that checkpoint are the same number.
 *
 * Shapes follow published curves: loss decays as a power law toward a floor
 * with a small warmup bump; LR is linear warmup then cosine; grad norm decays
 * with spikes at the known loss spike; evals rise with diminishing returns.
 */

import { CHANGES_FP8, EVAL_TASKS, hash, noise, rng, V3_NOW } from "./world";

export type EvalTask = (typeof EVAL_TASKS)[number];

/** Smooth deterministic jitter in [-1, 1] for (key, step). */
function jitter(key: string, step: number): number {
  return noise(rng(hash(`${key}@${Math.round(step)}`)));
}

// ── Hero run (llama-8b-support-v3) ────────────────────────────────────────────

/** Train loss of a v3 phase at a step (no noise: the "true" curve). */
export function v3LossCurve(phase: string, step: number): number {
  if (phase === "pretrain") return 1.55 + 0.95 * Math.pow(1 + step / 900, -0.42);
  if (phase === "sft") {
    const base = 1.72 + 0.62 * Math.pow(1 + step / 1600, -0.55);
    // The loss spike at 38,600–39,100 (bad shard 1184): rises, recovers.
    const spike = step > 38_600 && step < 39_100 ? 0.38 * Math.exp(-Math.pow((step - 38_720) / 120, 2)) : 0;
    return base + spike;
  }
  if (phase === "rl") return 0;
  return 0;
}

/** Train loss with per-step noise (what the chart draws). */
export const v3TrainLoss = (phase: string, step: number) => v3LossCurve(phase, step) + 0.012 * jitter(`v3-${phase}-loss`, step);

/** lm_loss (the language-model term without aux/z-loss): a hair under train loss. */
export const v3LmLoss = (phase: string, step: number) => v3TrainLoss(phase, step) - 0.006;

/** Val loss, logged every 500 steps; a touch above train. */
export const v3ValLoss = (phase: string, step: number) => v3LossCurve(phase, step) + 0.045 + 0.004 * jitter(`v3-${phase}-val`, step);

/** Learning rate: linear warmup over 2% then cosine to 10%. */
export function v3Lr(phase: string, step: number): number {
  const total = phase === "pretrain" ? 20_000 : 60_000;
  const peak = phase === "pretrain" ? 3e-4 : 2e-5;
  const warm = total * 0.02;
  if (step < warm) return (peak * step) / warm;
  const p = (step - warm) / (total - warm);
  return peak * (0.1 + 0.9 * 0.5 * (1 + Math.cos(Math.PI * Math.min(1, p))));
}

/** Global grad norm (pre-clip), with the spike at the bad shard. */
export function v3GradNorm(phase: string, step: number): number {
  const base = (phase === "pretrain" ? 0.9 : 0.55) * Math.pow(1 + step / 4000, -0.25) + 0.25;
  const spike = phase === "sft" && step > 38_600 && step < 39_100 ? 4.2 * Math.exp(-Math.pow((step - 38_710) / 90, 2)) : 0;
  return base + spike + 0.04 * jitter(`v3-${phase}-gn`, step);
}

/** Global tokens/s at a step of an attempt; the straggler costs ~15% after it starts. */
export function v3TokensPerSecond(phase: string, step: number, straggling: boolean): number {
  const base = phase === "pretrain" ? 1_180_000 : 1_090_000;
  return (straggling ? base * 0.85 : base) * (1 + 0.015 * jitter(`v3-${phase}-tps`, step));
}

/** MFU (bf16 dense, H100 989 TFLOP/s peak); 0–1. */
export const v3Mfu = (phase: string, step: number, straggling: boolean) => (v3TokensPerSecond(phase, step, straggling) * 6 * 8.03e9) / (64 * 989e12);

/** Step time in ms: tokens per step / tokens per second. */
export const v3StepTimeMs = (phase: string, step: number, straggling: boolean) => (V3_NOW.tokensPerStep / v3TokensPerSecond(phase, step, straggling)) * 1000;

/** The SFT step at which the straggler began (attempt 3). */
export const V3_STRAGGLER_STEP = 36_900;

// ── Evals ─────────────────────────────────────────────────────────────────────

/** Each task's metric, as a fraction of 1 (shown as a percent unless noted). */
const BASELINE: Record<EvalTask, number> = {
  mmlu_pro: 0.404,
  gsm8k: 0.712,
  ifeval: 0.78,
  arc_challenge: 0.612,
  truthfulqa: 0.489,
  long_context_ruler: 0.701,
  support_tickets_v3: 0.871,
  safety_refusals: 0.991,
};

/**
 * A subject's score on a task. Subjects: "v1", "v2" (production), "v2.1-fp8",
 * "v3@<step>" (a v3 SFT checkpoint), "gpt-4o-mini" (an external API baseline).
 */
export function evalScore(task: EvalTask, subject: string): number {
  const b = BASELINE[task];
  if (subject === "v2") return b;
  if (subject === "v1") return b - (task === "support_tickets_v3" ? 0.061 : task === "safety_refusals" ? 0.006 : 0.024);
  if (subject === "v2.1-fp8") {
    const d: Record<EvalTask, number> = { mmlu_pro: -0.003, gsm8k: -0.006, ifeval: -0.004, arc_challenge: -0.002, truthfulqa: -0.005, long_context_ruler: -0.011, support_tickets_v3: -0.002, safety_refusals: -0.012 };
    return b + d[task];
  }
  if (subject === "gpt-4o-mini") {
    const v: Record<EvalTask, number> = { mmlu_pro: 0.631, gsm8k: 0.873, ifeval: 0.838, arc_challenge: 0.92, truthfulqa: 0.61, long_context_ruler: 0.82, support_tickets_v3: 0.802, safety_refusals: 0.985 };
    return v[task];
  }
  const m = /^v3@(\d+)$/.exec(subject);
  if (m) {
    const step = Number(m[1]);
    const progress = 1 - Math.exp(-step / 14_000);
    // Where v3 lands at 40k against v2: better on most, GSM8K regresses (math data share fell), safety dips a hair.
    const final: Record<EvalTask, number> = { mmlu_pro: 0.008, gsm8k: -0.011, ifeval: 0.013, arc_challenge: 0.006, truthfulqa: 0.004, long_context_ruler: 0.009, support_tickets_v3: 0.013, safety_refusals: -0.004 };
    const start = b - 0.03;
    return start + (b + final[task] - start) * (progress / (1 - Math.exp(-40_000 / 14_000))) + 0.002 * jitter(`eval-${task}`, step);
  }
  return b;
}

/** The stderr of a score on a task with `n` samples (binomial). */
export const evalStderr = (score: number, n: number) => Math.sqrt((score * (1 - score)) / Math.max(1, n));

/** Samples per task. */
export const EVAL_SAMPLES: Record<EvalTask, number> = {
  mmlu_pro: 12_032,
  gsm8k: 1_319,
  ifeval: 541,
  arc_challenge: 1_172,
  truthfulqa: 817,
  long_context_ruler: 2_600,
  support_tickets_v3: 3_400,
  safety_refusals: 1_800,
};

// ── Serving ───────────────────────────────────────────────────────────────────

/** The fp8 arm's share of prod traffic. */
export const FP8_SHARE = CHANGES_FP8.trafficShare;

/** support-assistant-prod's request rate over the day (rps), peaking mid-afternoon UTC. */
export function prodRps(hourOfDayUtc: number): number {
  return 42 * (0.55 + 0.45 * Math.sin(((hourOfDayUtc - 9) / 24) * 2 * Math.PI)) + 3 * jitter("prod-rps", hourOfDayUtc * 60);
}
