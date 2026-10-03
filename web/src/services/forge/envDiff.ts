// Copyright (c) 2025 Reliant Labs

/**
 * WHAT THIS CHECKOUT WOULD CHANGE, PER ENVIRONMENT.
 *
 * Forge renders the environments declared in a checkout and compares each one
 * against what is deployed. The document is forge's and is rendered as-is: the
 * comparison belongs to forge, and a second implementation here would be a copy
 * that eventually disagrees with the thing that actually decides.
 *
 * THE STATUS IS NOT OPTIONAL READING. Every environment carries one, and three
 * of the five values mean "this diff is not an answer". An environment whose
 * render failed, or which has no recorded configuration to compare against,
 * reports that — and an empty diff and an unanswerable one mean opposite
 * things. Treating a failure as "nothing changed" is the single mistake this
 * module's shape exists to prevent, which is why diffIsAnswered is separate
 * from reading the counts.
 */

/**
 * Per-environment outcome. Forge's closed set.
 *
 * - `ok` — the render succeeded and the comparison is real.
 * - `impure` — the render WROTE files, so the result was discarded. Not a diff.
 * - `stale` — the checkout changed mid-render (someone was editing).
 * - `error` — the render failed.
 * - `unsupported` — this forge cannot render at all.
 */
export type EnvDiffStatus = "ok" | "impure" | "stale" | "error" | "unsupported" | "unknown";

/**
 * Decoded conservatively: an unrecognised status is `unknown`, NEVER `ok`.
 *
 * The direction is the safety property. A newer forge's additional status
 * decoded as ok would present whatever counts happened to be in the document as
 * a trustworthy diff, which is exactly the "a failure read as no changes"
 * failure. Unknown renders as "could not be determined" instead.
 */
export function envDiffStatusOf(value: string | undefined): EnvDiffStatus {
  switch (value) {
    case "ok":
      return "ok";
    case "impure":
      return "impure";
    case "stale":
      return "stale";
    case "error":
      return "error";
    case "unsupported":
      return "unsupported";
    default:
      return "unknown";
  }
}

/**
 * Whether this entry's diff is a real answer about what would change.
 *
 * Only `ok` qualifies. Everything else is the absence of an answer, and a
 * caller must say so rather than render the diff fields — which may be empty
 * for a reason that has nothing to do with the code being identical.
 */
export function diffIsAnswered(entry: ForgeEnvDiffEntry): boolean {
  return envDiffStatusOf(entry.status) === "ok";
}

/** Forge's shape diff. Counted, not interpreted. */
export interface ForgeShapeDiff {
  objects_added?: number;
  objects_removed?: number;
  objects_changed?: number;
  images_changed?: number;
  config_changed?: number;
  workloads_added?: string[];
  workloads_removed?: string[];
  secrets_needed?: string[];
  [key: string]: unknown;
}

export interface ForgeEnvDiffEntry {
  env?: string;
  status?: string;
  /** Forge's explanation, on a status that is not ok. */
  detail?: string;
  /** The files an impure render wrote. */
  wrote?: string[];
  diff?: ForgeShapeDiff;
  /** This environment does not exist yet; deploying would create it. */
  would_be_created?: boolean;
  /**
   * What the live side was read from. A diff against a recorded BUNDLE is
   * stronger evidence than one against a merely declared shape, and forge says
   * which so a reader can weigh it.
   */
  live_source?: string;
  config_identical?: boolean;
  /** Per-secret presence on the live side. */
  secret_presence?: Record<string, boolean>;
}

export interface ForgeEnvDiffReport {
  project?: string;
  /** Which checkout was rendered. */
  source?: string;
  /** What it was compared against. */
  against?: string;
  charts?: boolean;
  environments?: ForgeEnvDiffEntry[];
}

/** The entries, in forge's order. */
export function diffEntries(
  report: ForgeEnvDiffReport | null | undefined
): ForgeEnvDiffEntry[] {
  return report?.environments ?? [];
}

/**
 * Whether an answered diff found any difference at all.
 *
 * Returns false for an UNANSWERED entry too, which is why it must never be the
 * only thing a caller checks — "no changes found" and "we could not look" both
 * come back false here, and only diffIsAnswered separates them. The pairing is
 * deliberate: a single boolean conflating them is the bug.
 */
export function diffHasChanges(entry: ForgeEnvDiffEntry): boolean {
  if (!diffIsAnswered(entry)) return false;
  const diff = entry.diff;
  if (!diff) return false;
  const counts = [
    diff.objects_added,
    diff.objects_removed,
    diff.objects_changed,
    diff.images_changed,
    diff.config_changed,
  ];
  if (counts.some((count) => typeof count === "number" && count > 0)) return true;
  return (
    (diff.workloads_added?.length ?? 0) > 0 ||
    (diff.workloads_removed?.length ?? 0) > 0 ||
    (diff.secrets_needed?.length ?? 0) > 0
  );
}

/** The secret keys this checkout needs that are not present on the live side. */
export function missingSecrets(entry: ForgeEnvDiffEntry): string[] {
  const presence = entry.secret_presence ?? {};
  return Object.entries(presence)
    .filter(([, present]) => present === false)
    .map(([key]) => key)
    .sort();
}
