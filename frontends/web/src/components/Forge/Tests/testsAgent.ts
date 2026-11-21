// Copyright (c) 2026 Reliant Labs

/**
 * The Tests pages' agent actions, free of React: "Fix this flaky test" and
 * "Raise coverage of <pkg>" as AskReliant subjects (askReliantPrompt.ts
 * `flaky-test` and `coverage`), and when either can start (forge
 * docs/proposals/tests-coverage-flakes.md §4).
 *
 * Both start as Ask Reliant chats, sent at once on a workspace of their own
 * (the "Fix with agent" path). They later become the built-in workflows
 * fix-flaky-test and raise-coverage; nothing here knows about those.
 *
 * NO SOURCE. The coverage subject is built from coverage records only (paths,
 * figures, line numbers and hit counts), which hold no code; the agent reads
 * the code in its own checkout.
 */

import type { AskCoverageSubject, AskFlakyTestSubject, AskUncoveredFile } from "../Observability/AskReliant/askReliantPrompt";
import type { AskAgentMode } from "../Observability/AskReliant/askAgentMode";
import type { CoverageTree, FileCoverage, LineCoverage, TestDetail, TestFlake } from "@/services/controlPlane/tests/types";

// ── When an agent can start ─────────────────────────────────────────────────

export type AgentAvailability = { available: true } | { available: false; reason: string };

export const AGENT_REASON = {
  handoff: "Agents start from Reliant, where they work on a checkout of this project's repository. Open this project in Reliant to hand it to one.",
  noProject: "No Reliant project holds this app's repository yet. Add it as a project to hand this to an agent.",
  notGit: "This project's folder isn't a git repository, so an agent can't work on a branch of its own.",
} as const;

/**
 * Whether the Tests agent actions can start. They need Ask Reliant's chat
 * backend (not the handoff mode of the forge shell and demos) and a linked
 * repository: the Reliant project the console resolved for this forge project,
 * a git repository, since the agent works on a branch of its own. A project
 * the list has not answered for yet is not held against it: the action
 * resolves the project again when it starts.
 */
export function agentAvailability({
  mode,
  projectId,
  project,
}: {
  mode: AskAgentMode;
  projectId: string | null | undefined;
  project: { is_git_repo: boolean } | undefined;
}): AgentAvailability {
  if (mode === "handoff") return { available: false, reason: AGENT_REASON.handoff };
  if (!projectId) return { available: false, reason: AGENT_REASON.noProject };
  if (project && !project.is_git_repo) return { available: false, reason: AGENT_REASON.notGit };
  return { available: true };
}

// ── Fix this flaky test ─────────────────────────────────────────────────────

/** The flaky-test subject: the record the list shows, and the signatures only the test's own read (GetTest) has. */
export function flakyTestSubject(flake: TestFlake, detail: Pick<TestDetail, "signatures"> | null, defaultBranch?: string): AskFlakyTestSubject {
  const { test } = flake;
  const quarantine = flake.quarantine;
  return {
    kind: "flaky-test",
    env: null,
    testId: test.id,
    lang: test.lang,
    package: test.package,
    name: test.name,
    ...(test.file ? { file: test.file } : {}),
    ...(test.line > 0 ? { line: test.line } : {}),
    state: flake.state,
    flakeRate: flake.score,
    runs: flake.scoreRuns,
    confirmedEvents30d: flake.confirmedEvents30d,
    signatures: (detail?.signatures ?? []).map((s) => ({ signature: s.signature, count: s.count, verdict: s.verdict, sampleOutput: s.sampleOutput })),
    quarantine: quarantine ? { path: quarantine.path, ...(quarantine.reason ? { reason: quarantine.reason } : {}), expiresMs: quarantine.expiresMs, overdue: quarantine.overdue } : null,
    ...(flake.issueUrl || quarantine?.issueUrl ? { issueUrl: flake.issueUrl || quarantine!.issueUrl } : {}),
    ...(defaultBranch ? { defaultBranch } : {}),
  };
}

// ── Raise coverage of <pkg> ─────────────────────────────────────────────────

/** The most files whose uncovered lines a coverage subject lists (lowest-covered first); the prompt names the command for the rest. */
export const MAX_UNCOVERED_FILES = 8;

/**
 * The combined percent a coverage agent aims for when nobody set one: the
 * next multiple of ten above where the package is now (58.0% → 60, 60.0% →
 * 70), capped at 100; 50 for a package with no figure yet. A step the agent
 * can reach in one change, stated in the prompt so the reader sees it.
 */
export function defaultCoverageTarget(current: number | null): number {
  if (current === null) return 50;
  return Math.min(100, Math.floor(current / 10) * 10 + 10);
}

/** A line no lane reached: it has statements, and no hits. */
const uncoveredLine = (line: LineCoverage) => line.statements > 0 && Object.values(line.hits).every((hits) => !hits);

/** A file's uncovered lines as inclusive ranges of consecutive line numbers. */
export function uncoveredRanges(lines: readonly LineCoverage[]): [number, number][] {
  const numbers = lines
    .filter(uncoveredLine)
    .map((line) => line.line)
    .sort((a, b) => a - b);
  const ranges: [number, number][] = [];
  for (const n of numbers) {
    const last = ranges.at(-1);
    if (last && n === last[1] + 1) last[1] = n;
    else if (!last || n > last[1]) ranges.push([n, n]);
  }
  return ranges;
}

/** The files under a coverage listing that have uncovered statements, lowest-covered first: the ones a subject reads line by line. */
export function filesToRead(tree: CoverageTree): string[] {
  return tree.children
    .filter((child) => child.kind === "file" && child.combined && child.combined.covered < child.combined.statements)
    .sort((a, b) => a.combined!.percent - b.combined!.percent || a.path.localeCompare(b.path))
    .map((child) => child.path);
}

/**
 * The coverage subject for a package: its combined and per-lane figures from
 * its listing (GetCoverage), and the uncovered line numbers of the files read
 * (GetFileCoverage, in `filesToRead` order). Built from coverage records
 * alone, so no code can reach it.
 */
export function coverageSubject({
  pkg,
  tree,
  files,
  target,
  defaultBranch,
}: {
  pkg: string;
  tree: CoverageTree;
  /** The files read, in `filesToRead(tree)` order (at most MAX_UNCOVERED_FILES). */
  files: readonly FileCoverage[];
  target?: number;
  defaultBranch?: string;
}): AskCoverageSubject {
  const combined = tree.coverage?.combined ?? null;
  const percent = combined && combined.statements > 0 ? combined.percent : null;
  const uncovered: AskUncoveredFile[] = files.map((file) => ({ path: file.path, lines: uncoveredRanges(file.lines) })).filter((file) => file.lines.length > 0);
  const lanes = Object.fromEntries(
    Object.entries(tree.coverage?.lanes ?? {})
      .filter(([, figure]) => figure.statements > 0)
      .map(([lane, figure]) => [lane, figure.percent])
  );
  return {
    kind: "coverage",
    env: null,
    package: pkg,
    commit: tree.gitCommit,
    combined: percent,
    statements: combined?.statements ?? 0,
    covered: combined?.covered ?? 0,
    lanes,
    uncovered,
    moreFiles: Math.max(0, filesToRead(tree).length - files.length),
    target: target ?? defaultCoverageTarget(percent),
    ...(defaultBranch ? { defaultBranch } : {}),
  };
}
