// Copyright (c) 2026 Reliant Labs

/**
 * What the Coverage page computes, free of React: where a path sits in the
 * tree, whose report a URL names, a line's state through the page's lens
 * (one lane, or every lane), and the words for each no-source state.
 */

import type { CodeLineTone } from "@/components/shared/LightweightCodeViewer";
import type { CoverageSourceUnavailableReason } from "@/services/coverageSource/coverageSource";
import type { CoverageNode, CoverageTarget, LineCoverage, StatementCoverage } from "@/services/controlPlane/tests/types";

/** The Coverage page's URL (routeSchemas forgeConsoleTestsCoverageSearchSchema). Every key optional; the bare URL is the default view. */
export interface CoverageSearch {
  /** Repo-relative; unset is the root. A file path too: it rides the search, never a segment. */
  path?: string;
  /** Whose report. Unset: the scoped environment's current release, else the newest report of the default branch. */
  commit?: string;
  /** Only this lane. Unset: combined, every lane beside it. */
  lane?: string;
  /** Diff coverage against this commit. Unset: none. */
  base?: string;
}

/** A path's parent: "internal/billing/refund.go" → "internal/billing"; a top-level path → "" (the root). */
export function parentPath(path: string): string {
  const cut = path.lastIndexOf("/");
  return cut === -1 ? "" : path.slice(0, cut);
}

/** The breadcrumb of a path: the root, then each prefix. */
export function pathCrumbs(path: string): { label: string; path: string }[] {
  const crumbs = [{ label: "All code", path: "" }];
  if (!path) return crumbs;
  const parts = path.split("/").filter(Boolean);
  parts.forEach((part, i) => crumbs.push({ label: part, path: parts.slice(0, i + 1).join("/") }));
  return crumbs;
}

/** A child's name under the path it is listed in: the rest of its path, so "internal/billing" under the root stays whole. */
export function childName(child: string, parent: string): string {
  return parent && child.startsWith(`${parent}/`) ? child.slice(parent.length + 1) : child;
}

/**
 * Whose report the page reads. A commit in the URL wins; else the scoped
 * environment's current release; else the newest report the default
 * branch has (null until the summary says which, or when there is none).
 */
export function coverageTarget(commit: string | undefined, environmentId: string | undefined, newestCommit: string | null): CoverageTarget | null {
  if (commit) return { gitCommit: commit };
  if (environmentId) return { environmentId };
  return newestCommit ? { gitCommit: newestCommit } : null;
}

/** A statement figure as words, never 0% for nothing: "74.2%", or "—" with no statements. */
export function percentOf(coverage: StatementCoverage | null | undefined): string {
  if (!coverage || coverage.statements === 0) return "—";
  return `${(Math.round(coverage.percent * 10) / 10).toFixed(1)}%`;
}

/** A count in a narrow column: 12, 1.2k, 48k. */
export function compactCount(count: number): string {
  if (count < 1_000) return String(count);
  if (count < 10_000) return `${(Math.floor(count / 100) / 10).toFixed(1)}k`;
  if (count < 1_000_000) return `${Math.floor(count / 1_000)}k`;
  return `${Math.floor(count / 100_000) / 10}M`;
}

/** Packages and directories before files, each by path: the order the tree reads in, whatever the server sent. */
export function sortNodes(nodes: CoverageNode[]): CoverageNode[] {
  const rank = (node: CoverageNode) => (node.kind === "file" ? 1 : 0);
  return [...nodes].sort((a, b) => rank(a) - rank(b) || a.path.localeCompare(b.path));
}

export type LineState = "covered" | "uncovered" | "partial";

/**
 * A line's state through the lens: `lane` set reads only that lane's hits,
 * unset reads every lane (a line any lane reached is covered). Null for a
 * line that holds no statement. Partial needs a hit: a line nothing reached
 * is uncovered, whatever its blocks.
 */
export function lineState(line: LineCoverage, lane?: string): LineState | null {
  if (line.statements === 0) return null;
  const hits = lane ? (line.hits[lane] ?? 0) : Object.values(line.hits).reduce((sum, h) => sum + h, 0);
  if (hits === 0) return "uncovered";
  return line.partial ? "partial" : "covered";
}

/** Each state's tone in the code viewer: a status surface (the data a line carries), the gutter says the number. */
export const LINE_TONES: Record<LineState, CodeLineTone> = {
  covered: { name: "covered", className: "bg-success-surface" },
  uncovered: { name: "uncovered", className: "bg-danger-surface" },
  partial: { name: "partial", className: "bg-warning-surface" },
};

/** The highest line the report covers: the source must reach at least this far to be the file the tests ran on. */
export function lastCoveredLine(lines: LineCoverage[]): number {
  return lines.reduce((max, line) => Math.max(max, line.line), 0);
}

/** A file's text as the viewer numbers it: the final newline ends the last line, it does not open an empty one. */
export function sourceText(text: string): string {
  return text.endsWith("\n") ? text.slice(0, -1) : text;
}

/** prism's language for a report's `lang` (or the file's extension when it names none). */
export function viewerLanguage(lang: string, path: string): string {
  const known: Record<string, string> = { go: "go", ts: "typescript", typescript: "typescript", tsx: "typescript", js: "javascript", javascript: "javascript", py: "python", python: "python" };
  const ext = path.slice(path.lastIndexOf(".") + 1).toLowerCase();
  return known[lang.toLowerCase()] ?? known[ext] ?? "plaintext";
}

/**
 * The one line a file view says when it has no source, by reason. Each names
 * what happened and, when the reader can do something, what.
 */
export function noSourceWords(reason: CoverageSourceUnavailableReason): string {
  switch (reason) {
    case "no-repo":
      return "Source isn't shown: this project has no linked GitHub repository. Link one to see each line's code beside its hits.";
    case "uncommitted":
      return "Source isn't shown: the run had uncommitted changes to this file, so its version is in no commit GitHub has.";
    case "gone":
      return "Source isn't shown: GitHub doesn't show you this version of the file (its history was rewritten, the repository moved or was deleted, or your GitHub account can't read it).";
    case "revoked":
      return "Source isn't shown: your GitHub isn't connected, or its connection was revoked. Connect GitHub to see source.";
    case "not-yet-available":
      return "Source isn't shown here yet: this deployment can't read files from your linked repository. The hits below are complete.";
    case "too-large":
      return "Source isn't shown: this file is larger than the coverage view shows (2 MiB). The hits below are complete.";
    case "mismatch":
      return "Source isn't shown: what GitHub returned is not the version these tests ran on, and coverage drawn over other code would be wrong.";
    case "unreachable":
      return "Source isn't shown: GitHub didn't answer. The hits below are complete.";
  }
}
