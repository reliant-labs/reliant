// Copyright (c) 2025 Reliant Labs

/**
 * The Security pages' vocabulary and decisions, free of React: labels for the
 * enums, what the URL's filters mean to the server, and the one rule the
 * pages exist to keep — UNKNOWN IS NEVER CLEAN. "No known vulnerabilities" is
 * said only when every environment in scope has a release whose every
 * artifact has an SBOM scanned against a current database, and the scan found
 * nothing.
 */

import type {
  EnvVulnSummary,
  FixState,
  MinSeverity,
  ScanState,
  SbomSource,
  Severity,
  SeverityCounts,
  VulnFilters,
} from "@/services/controlPlane/vulnerabilities/types";
import { MIN_SEVERITIES } from "@/services/controlPlane/vulnerabilities/types";

/**
 * Where the vulnerability and SBOM docs live, spelled once. Releases and
 * promotion until a page of their own is written (the vuln_gate is there).
 */
export const VULN_DOCS_URL = "https://docs.reliantlabs.io/forge/deploy/releases-and-promotion";

export const SEVERITY_LABEL: Record<Severity, string> = {
  critical: "Critical",
  high: "High",
  medium: "Medium",
  low: "Low",
  negligible: "Negligible",
  unknown: "Unknown",
};

/** forge-ui Status tones: only critical and high are a problem at a glance; an unrated one is not hidden as calm either. */
export const SEVERITY_TONE: Record<Severity, "danger" | "warning" | "neutral"> = {
  critical: "danger",
  high: "danger",
  medium: "warning",
  low: "neutral",
  negligible: "neutral",
  unknown: "warning",
};

export const SCAN_STATE_LABEL: Record<ScanState, string> = {
  none: "Nothing deployed",
  "no-sbom": "Not scanned",
  pending: "Scan pending",
  scanned: "Scanned",
  failed: "Scan failed",
  stale: "Stale database",
};

/** How an SBOM's source reads beside it. A lockfile SBOM lists what the app depends on, not what the bundle ships. */
export function sbomSourceLabel(source: SbomSource): string {
  switch (source) {
    case "lockfile":
      return "from lockfile";
    case "image":
      return "from image";
    case "registry":
      return "from registry image";
    case "binary":
      return "from binary";
    case "ci-provided":
      return "from CI";
    default:
      return source ? `from ${source}` : "";
  }
}

/** The package's fix, in words: "→ 1.2.3", "no fix yet", "won't be fixed". */
export function fixLabel(fixState: FixState, fixedVersions: readonly string[]): string {
  switch (fixState) {
    case "fixed":
      return fixedVersions.length ? `fixed in ${fixedVersions.join(", ")}` : "a fix exists";
    case "not-fixed":
      return "no fix yet";
    case "wont-fix":
      return "won't be fixed";
    default:
      return "fix unknown";
  }
}

export function totalFindings(counts: SeverityCounts): number {
  return counts.critical + counts.high + counts.medium + counts.low + counts.negligible + counts.unknown;
}

/** EPSS as a percentage a person reads: "12%", "<1%". */
export function formatEpss(epss: number): string {
  const pct = epss * 100;
  if (pct > 0 && pct < 1) return "<1%";
  return `${Math.round(pct)}%`;
}

// ── The URL's filters ───────────────────────────────────────────────────────

/** The Vulnerabilities page's own search keys (beside the console's `env` scope). */
export interface VulnSearch {
  /** A release version instead of the scope's current releases. */
  release?: string;
  /** Minimum severity. */
  severity?: MinSeverity;
  /** "yes": only fixable; "no": only not. */
  fixable?: "yes" | "no";
  /** "1": only KEV-listed. */
  kev?: "1";
  /** "1": exempt findings too. */
  exempt?: "1";
}

export const VULN_SEARCH_KEYS = ["release", "severity", "fixable", "kev", "exempt"] as const satisfies readonly (keyof VulnSearch)[];

/** The VulnSearch keys of a raw search object; a value that does not fit is absent. */
export function pickVulnSearch(raw: Record<string, unknown>): VulnSearch {
  const out: VulnSearch = {};
  if (typeof raw.release === "string" && raw.release) out.release = raw.release;
  if (typeof raw.severity === "string" && (MIN_SEVERITIES as readonly string[]).includes(raw.severity)) out.severity = raw.severity as MinSeverity;
  if (raw.fixable === "yes" || raw.fixable === "no") out.fixable = raw.fixable;
  if (raw.kev === "1" || raw.kev === 1) out.kev = "1";
  if (raw.exempt === "1" || raw.exempt === 1) out.exempt = "1";
  return out;
}

/** How many of the page's own filters are set (the scope is not one of them). */
export function activeFilterCount(search: VulnSearch): number {
  return VULN_SEARCH_KEYS.filter((key) => search[key] !== undefined).length;
}

/** The request's optional fields, from the scope's environment id (none for all) and the page's filters. */
export function filtersOf(environmentId: string | undefined, search: VulnSearch): VulnFilters {
  const filters: VulnFilters = {};
  if (environmentId) filters.environmentId = environmentId;
  if (search.release) filters.releaseVersion = search.release;
  if (search.severity) filters.minSeverity = search.severity;
  if (search.fixable) filters.fixable = search.fixable === "yes";
  if (search.kev) filters.kev = true;
  if (search.exempt) filters.includeExempt = true;
  return filters;
}

// ── Whether the findings can be trusted ─────────────────────────────────────

/**
 * What the page says about the scope as a whole, from its environments'
 * summaries:
 *
 *   not-scanned-yet  no environment in scope has a scanned release (nothing
 *                    deployed, or only releases without SBOMs, or every scan
 *                    still pending): the "not scanned yet" page
 *   partial          some findings can be trusted, but not every environment's
 *                    release is fully scanned against a current database: the
 *                    table, with a note per environment that is not
 *   trusted          every environment in scope with a release is scanned:
 *                    an empty table is "no known vulnerabilities"
 */
export type ScopeTrust = "not-scanned-yet" | "partial" | "trusted";

/** Environments that have a release: the ones there is anything to say about. */
export function deployedEnvs(envs: readonly EnvVulnSummary[]): EnvVulnSummary[] {
  return envs.filter((env) => env.releaseId !== "" && env.scanState !== "none");
}

/** States with findings someone can read (possibly from an older scan). */
function hasFindings(env: EnvVulnSummary): boolean {
  return env.scanState === "scanned" || env.scanState === "stale" || (env.scanState === "failed" && env.lastScannedMs !== null) || env.coverage.scanned > 0;
}

export function scopeTrust(envs: readonly EnvVulnSummary[]): ScopeTrust {
  const deployed = deployedEnvs(envs);
  if (deployed.length === 0 || !deployed.some(hasFindings)) return "not-scanned-yet";
  return deployed.every((env) => env.scanState === "scanned") ? "trusted" : "partial";
}

/** The environments in scope whose findings fall short, each to be explained. */
export function untrustedEnvs(envs: readonly EnvVulnSummary[]): EnvVulnSummary[] {
  return deployedEnvs(envs).filter((env) => env.scanState !== "scanned");
}
