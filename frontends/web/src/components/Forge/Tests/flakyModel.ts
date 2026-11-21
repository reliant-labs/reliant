// Copyright (c) 2026 Reliant Labs

/**
 * What Tests › Flaky tests and a test's page compute from TestsService's
 * answers, free of React: the URL's state filter, a flake state in words,
 * the flake rate with its n, the quarantine command, and the outcome strip.
 *
 * The rules they follow (forge docs/proposals/tests-coverage-flakes.md §1.5,
 * phase-0 amendments 6 and 7):
 *
 *   - `suspected` (one confirmed event) is the state people act on: our
 *     repos delete a flaky test on its first event. `flaky` is two or more
 *     confirmed events on distinct commits in 30 days.
 *   - The flake rate (a Wilson lower bound) RANKS tests and is shown with its
 *     n. It never moves a test between states, so it is never worded as a
 *     verdict.
 *   - `host` and `correlated` failures never score: they are host health and
 *     infrastructure events, listed apart from the tests.
 */

import type { TestFlake, TestFlakeSignal, TestFlakeState, TestOutcome } from "@/services/controlPlane/tests/types";

/** The `state` values the URL accepts (ListFlakyTests' state filter). Unset: every state but healthy. */
export const FLAKY_STATE_FILTERS = ["suspected", "flaky", "quarantined", "fixed"] as const satisfies readonly TestFlakeState[];
export type FlakyStateFilter = (typeof FLAKY_STATE_FILTERS)[number];

/** Tests › Flaky tests' own filter, as the URL holds it. */
export interface FlakySearch {
  state?: FlakyStateFilter;
}

export const FLAKY_SEARCH_KEYS = ["state"] as const satisfies readonly (keyof FlakySearch)[];

export function pickFlakySearch(raw: Record<string, unknown>): FlakySearch {
  return (FLAKY_STATE_FILTERS as readonly unknown[]).includes(raw.state) ? { state: raw.state as FlakyStateFilter } : {};
}

export type StateTone = "success" | "warning" | "danger" | "neutral";

/** Each state's word, its dot, and what it means for the reader. */
export const FLAKE_STATE: Record<TestFlakeState, { label: string; tone: StateTone; means: string }> = {
  suspected: { label: "Suspected", tone: "warning", means: "One confirmed flake. Act on it now: delete or rewrite the test, or quarantine it." },
  flaky: { label: "Flaky", tone: "danger", means: "Confirmed flakes on two or more commits in 30 days." },
  quarantined: { label: "Quarantined", tone: "neutral", means: "Skipped by the project's lanes until its quarantine entry is removed." },
  fixed: { label: "Fixed", tone: "neutral", means: "Was flaky; its file changed and 14 days passed without a flake." },
  removed: { label: "Removed", tone: "neutral", means: "Was flaky, and no longer runs." },
  healthy: { label: "Healthy", tone: "neutral", means: "No confirmed flake in the last 30 days." },
  unknown: { label: "Unknown", tone: "neutral", means: "This client does not know the state the server sent." },
};

export const FLAKY_STATE_FILTER_LABEL: Record<FlakyStateFilter, string> = {
  suspected: "Suspected",
  flaky: "Flaky",
  quarantined: "Quarantined",
  fixed: "Fixed",
};

export const SIGNAL_LABEL: Record<TestFlakeSignal, string> = {
  "same-tree": "same-tree split",
  "retry-pass": "retry pass",
  confirm: "flake confirm",
  history: "history",
  unknown: "unknown signal",
};

/**
 * A flake rate (0–1) as a percentage, two significant figures so small rates
 * still rank: 0.0023 → "0.23%", 0.114 → "11%".
 */
export function formatFlakeRate(score: number): string {
  if (!(score > 0)) return "0%";
  const percent = score * 100;
  if (percent < 0.01) return "<0.01%";
  return `${Number(percent.toPrecision(2))}%`;
}

/** "0.23% (412 runs)": the rate always with its n, and no rate over no runs. */
export function flakeRateWords(flake: Pick<TestFlake, "score" | "scoreRuns">): string {
  if (flake.scoreRuns <= 0) return "— (no runs yet)";
  return `${formatFlakeRate(flake.score)} (${flake.scoreRuns.toLocaleString()} ${flake.scoreRuns === 1 ? "run" : "runs"})`;
}

/** What the flake rate is, for its header and tooltips. */
export const FLAKE_RATE_MEANS =
  "The share of commits the test flaked on, of those it ran on in the default branch's last 30 days (a Wilson lower bound, so few runs rank low). A ranking, never a verdict: the state comes from confirmed events.";

/** Whether a quarantine is the suggested next step: a suspected or flaky test with no entry yet. */
export function suggestsQuarantine(flake: Pick<TestFlake, "state" | "quarantine">): boolean {
  return (flake.state === "suspected" || flake.state === "flaky") && !flake.quarantine;
}

/** POSIX single-quoting, when the value needs it: a test id with a subtest's space stays one argument. */
function shellArg(value: string): string {
  return /^[\w@%+=:,./-]+$/.test(value) ? value : `'${value.replace(/'/g, `'\\''`)}'`;
}

/** The command that quarantines the test (forge test quarantine add), with its issue when it has one. */
export function quarantineCommand(flake: Pick<TestFlake, "test" | "issueUrl">): string {
  const issue = flake.issueUrl ? ` --issue ${shellArg(flake.issueUrl)}` : "";
  return `reliant forge test quarantine add ${shellArg(flake.test.id)}${issue}`;
}

/** "#12" from a GitHub issue URL; "Issue" otherwise. */
export function issueLabel(url: string): string {
  const match = /\/issues\/(\d+)(?:[/?#]|$)/.exec(url);
  return match ? `#${match[1]}` : "Issue";
}

/** One run in the outcome strip. A failure that passed on retry is its own kind: it is what a flake looks like. */
export type StripKind = "pass" | "retry" | "fail" | "skip" | "unknown";

export function stripKind(outcome: Pick<TestOutcome, "status" | "retryPassed">): StripKind {
  if (outcome.retryPassed) return "retry";
  return outcome.status === "pass" || outcome.status === "fail" || outcome.status === "skip" ? outcome.status : "unknown";
}

export const STRIP_LABEL: Record<StripKind, string> = {
  pass: "passed",
  retry: "failed, then passed on retry",
  fail: "failed",
  skip: "skipped",
  unknown: "unknown",
};

/** The strip, oldest first (the API answers newest first), and how many of each kind it holds. */
export function outcomeStrip(outcomes: TestOutcome[]): { cells: (TestOutcome & { kind: StripKind })[]; counts: Record<StripKind, number> } {
  const counts: Record<StripKind, number> = { pass: 0, retry: 0, fail: 0, skip: 0, unknown: 0 };
  const cells = [...outcomes].reverse().map((outcome) => {
    const kind = stripKind(outcome);
    counts[kind] += 1;
    return { ...outcome, kind };
  });
  return { cells, counts };
}
