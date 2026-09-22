// Copyright (c) 2025 Reliant Labs

/**
 * Forge env-status data layer — `forge env status <env> --json`.
 *
 * This module owns the shape of that document and, far more importantly, the
 * mapping from a check's `status` to a DISPOSITION the UI can paint. No React,
 * no styling; presentation lives in components/Forge/Status.
 *
 * FIVE STATUSES, FIVE DISPOSITIONS. Nothing is merged here.
 *
 *   pass     measured-good     measured, and it answered correctly
 *   fail     measured-bad      measured, and it answered wrongly
 *   warn     measured-degraded measured, and the answer is imperfect
 *   skip     not-applicable    asked and ANSWERED: this does not apply here
 *   unknown  undetermined      COULD NOT OBTAIN THE FACTS. No answer at all.
 *
 * The two that must never collapse into anything else are `skip` and `unknown`,
 * and they must not collapse into EACH OTHER. Forge's own doctor package says so
 * in a comment at the top of its Status enum, and it says it because the two
 * used to render as the same grey dash: `App Health  app port 8080 not
 * discovered` was byte-identical to `tool: mkcert  not required for this
 * project`. One of those is a finished answer and the other is a hole in the
 * report.
 *
 * `unknown` is the one with teeth. Forge rolls it up to Warn — never to Pass —
 * because a check that could not look has established nothing. `forge env status
 * dev` once reported all-green for an hour while daemon-gateway was OOMKilled
 * and in CrashLoopBackOff, and for ten hours while three more services
 * crashlooped. Painting "could not measure" as green IS that outage's UI.
 *
 * `warn` is deliberately its OWN disposition rather than a flavour of bad: it
 * was measured (so it is certain, unlike unknown) but it is not a clean pass
 * (so it is not good). Folding it into `fail` would make a degraded stack
 * indistinguishable from a broken one.
 *
 * An unrecognised status from a newer forge lands in `undetermined`. That
 * fallback direction is load-bearing: guessing "good" for a status this build
 * cannot read is the single wrong answer, because it is indistinguishable from
 * a verified pass.
 */

import type { ForgeClusterInventory } from "./workloads";

// ── Check status and disposition ────────────────────────────────────────────

/** The five statuses forge's doctor package can put on a check result. */
export type ForgeCheckStatus = "pass" | "fail" | "warn" | "skip" | "unknown";

/**
 * What a check's status actually tells you. Five values, one per status,
 * because every merge available here has been a real bug — see the module
 * comment.
 */
export type CheckDisposition =
  /** Measured; it answered correctly. */
  | "measured-good"
  /** Measured; it answered wrongly. */
  | "measured-bad"
  /** Measured; the answer is imperfect but the measurement happened. */
  | "measured-degraded"
  /** Asked and answered: the project's own shape means this does not apply. */
  | "not-applicable"
  /** No answer at all — the facts the check needed were not obtainable. */
  | "undetermined";

const DISPOSITION_BY_STATUS: Record<ForgeCheckStatus, CheckDisposition> = {
  pass: "measured-good",
  fail: "measured-bad",
  warn: "measured-degraded",
  skip: "not-applicable",
  unknown: "undetermined",
};

/**
 * dispositionOf classifies a check status. An absent or unrecognised status is
 * `undetermined`, never `measured-good`.
 */
export function dispositionOf(status: string | undefined): CheckDisposition {
  if (!status) return "undetermined";
  return DISPOSITION_BY_STATUS[status as ForgeCheckStatus] ?? "undetermined";
}

/** True when forge actually performed a measurement and got an answer. */
export function wasMeasured(status: string | undefined): boolean {
  return status === "pass" || status === "fail" || status === "warn";
}

/**
 * Short human label per status. Never collapses two statuses onto one word, and
 * `skip`/`unknown` get wordings that cannot be mistaken for one another:
 * "Not applicable" is a conclusion, "Could not measure" is the absence of one.
 */
export function checkStatusLabel(status: string | undefined): string {
  switch (status) {
    case "pass":
      return "Pass";
    case "fail":
      return "Fail";
    case "warn":
      return "Warn";
    case "skip":
      return "Not applicable";
    case "unknown":
      return "Could not measure";
    default:
      return "Unrecognised";
  }
}

/**
 * One-line explanation of what a status means. The `skip` and `unknown`
 * sentences carry the whole distinction, so each says explicitly what the other
 * is not.
 */
export function checkStatusExplanation(status: string | undefined): string {
  switch (status) {
    case "pass":
      return "The check ran, obtained its facts, and they were correct.";
    case "fail":
      return "The check ran, obtained its facts, and they were wrong. This is a real problem.";
    case "warn":
      return "The check ran and obtained its facts; they are not clean, but the measurement happened.";
    case "skip":
      return "The check asked its question and this project's shape answers it: it does not apply here. Nothing is missing.";
    case "unknown":
      return "The check could not obtain a fact it needed, so it has no answer. This is neither a pass nor a skip — it is a hole in the report.";
    default:
      return "This build of reliant does not recognise the status forge reported, so it is treated as undetermined rather than as a pass.";
  }
}

// ── Forge's env status document ─────────────────────────────────────────────
//
// Typed against forge's `env status --json` envelope (upServicesReport), whose
// contract is explicitly ADDITIVE: a new key never changes the meaning of an
// existing one. Everything forge marks `omitempty` is optional here, and
// nothing is assumed non-null — a report from an older or newer forge must
// render, not throw.

export interface ForgeCheckResult {
  name?: string;
  status?: string;
  message?: string;
  /** Diagnostic text for a human. Displayed, but NEVER branched on. */
  evidence?: string;
  duration_ms?: number;
}

/** One duplicated command under a service row. */
export interface ForgeDuplicateGroup {
  command?: string;
  pids?: number[];
}

/** One live server process backing a host service, with build freshness. */
export interface ForgeServingProc {
  pid?: number;
  path?: string;
  built_at?: string;
  started_at?: string;
  argv?: string[];
  command?: string;
  stale?: boolean;
}

export interface ForgeServiceRow {
  name?: string;
  /** "host" | "frontend". */
  kind?: string;
  url?: string;
  port?: number;
  log?: string;
  listening?: boolean;
  pid?: number;
  owned?: boolean;
  serving?: ForgeServingProc[];
  duplicate?: boolean;
  duplicates?: ForgeDuplicateGroup[];
  /**
   * Forge could not read argv for at least one process, so it declines to
   * attribute the duplicate rather than guessing. A third answer, like
   * `unknown` above — not a "no".
   */
  attribution_undetermined?: boolean;
}

export interface ForgeEnvStatusReport {
  env?: string;
  database_url?: string;
  head_commit_at?: string;
  /**
   * The HOST PROCESSES on the reader's own machine. NOT a deployment
   * inventory, and the distinction is not pedantry: rendering this array under
   * a heading that read like one is what showed two local dev servers for a
   * prod that runs sixteen cluster workloads. The cluster's real contents are
   * `workloads` below.
   */
  services?: ForgeServiceRow[];
  /**
   * The CLUSTER INVENTORY — what this environment actually deploys. A
   * DOCUMENT, not an array: its `status` says whether the workload list is an
   * inventory at all, because forge lists workloads it could not see. Optional
   * because a forge predating it omits the key entirely, which is a third
   * answer ("nobody was asked") and must not render as "deploys nothing".
   * See ./workloads, which owns this shape and the branch it requires.
   */
  workloads?: ForgeClusterInventory;
  /**
   * The env-runtime checks — eight of them across five signals (app, metrics,
   * traces, logs, profiles), plus the compose-infra check every signal keeps.
   */
  checks?: ForgeCheckResult[];
  /** Forge's own roll-up. Recomputed locally so it cannot disagree with the rows. */
  overall?: string;
  duration_ms?: number;
}

// ── Projection (still pure) ─────────────────────────────────────────────────

/**
 * checksOf returns the check rows in forge's own order, with NOTHING filtered
 * out.
 *
 * There is no option to hide a check, and that is deliberate. Forge puts the
 * cluster-workload probe inside the `app` signal precisely because a panel that
 * reports on "app" while ignoring every pod forge itself applied is the report
 * that hid two multi-hour outages. A tidier panel is the bug.
 *
 * A row with no name still renders; it is given a positional label rather than
 * being dropped, because a check forge emitted and we could not label is itself
 * information.
 */
export function checksOf(
  report: ForgeEnvStatusReport | null | undefined
): Array<ForgeCheckResult & { name: string }> {
  if (!report || !Array.isArray(report.checks)) return [];
  return report.checks
    .filter((check): check is ForgeCheckResult => !!check && typeof check === "object")
    .map((check, index) => ({ ...check, name: check.name || `Check ${index + 1}` }));
}

/**
 * dispositionTally counts the checks per disposition, summed from the rows the
 * panel renders rather than read from `report.overall`, so the header can never
 * disagree with the list underneath it.
 */
export function dispositionTally(
  report: ForgeEnvStatusReport | null | undefined
): Record<CheckDisposition, number> {
  const totals: Record<CheckDisposition, number> = {
    "measured-good": 0,
    "measured-bad": 0,
    "measured-degraded": 0,
    "not-applicable": 0,
    undetermined: 0,
  };
  for (const check of checksOf(report)) {
    totals[dispositionOf(check.status)] += 1;
  }
  return totals;
}

/**
 * The panel's one-line verdict. Five values, and the ordering encodes forge's
 * own roll-up plus the rule this surface exists for.
 *
 * `undetermined` outranks `degraded` on purpose: a run with holes in it has not
 * earned a verdict at all, and saying "degraded" would imply the rest was
 * confirmed fine. Only `all-good` claims health, and it requires that every
 * check either passed or legitimately did not apply.
 */
export type EnvStatusVerdict =
  | "failing"
  | "incomplete"
  | "degraded"
  | "all-good"
  /** Forge emitted no checks at all. Not health — the absence of a report. */
  | "no-checks";

export function verdictOf(report: ForgeEnvStatusReport | null | undefined): EnvStatusVerdict {
  const checks = checksOf(report);
  if (checks.length === 0) return "no-checks";
  const tally = dispositionTally(report);
  if (tally["measured-bad"] > 0) return "failing";
  if (tally.undetermined > 0) return "incomplete";
  if (tally["measured-degraded"] > 0) return "degraded";
  return "all-good";
}

/**
 * verdictSentence says what the verdict means, in the panel, in words. The
 * `incomplete` sentence is the one that stops a reader treating a run full of
 * holes as a clean bill of health.
 */
export function verdictSentence(verdict: EnvStatusVerdict, env: string): string {
  switch (verdict) {
    case "failing":
      return `At least one check measured ${env} and found a real problem.`;
    case "incomplete":
      return `Some checks could not obtain the facts they needed, so the state of ${env} is partly unknown. This is not a clean bill of health.`;
    case "degraded":
      return `Every check reported, and at least one is not clean. Nothing came back undetermined.`;
    case "all-good":
      return `Every check that applies to ${env} was measured and passed.`;
    case "no-checks":
      return `Forge returned no runtime checks for ${env}, so nothing here has been measured.`;
  }
}

/** formatDuration renders a check's duration without implying precision forge did not give. */
export function formatCheckDuration(ms: number | undefined): string {
  if (typeof ms !== "number" || !Number.isFinite(ms) || ms < 0) return "";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}
