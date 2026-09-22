// Copyright (c) 2025 Reliant Labs

/**
 * The join: what the environment DECLARES against what the store HOLDS.
 *
 * This module exists because neither source is the whole truth, and the screen's
 * most useful row — "a workload needs this and nobody has ever set it" — is
 * visible in neither one alone:
 *
 *   forge (services/forge/secrets.ts)  knows which secrets a workload declares,
 *                                      by reading the KCL. It has no idea what
 *                                      the managed store contains.
 *   the managed store (secretStore.ts) knows which secrets exist and how many
 *                                      times each was written. It has no idea
 *                                      which of them anything actually uses.
 *
 * So a declared-but-unset secret is a row that exists in forge's list and not in
 * the store's, and an orphan is the reverse. Both are real states worth naming,
 * and both are invisible from one side.
 *
 * ── MODE IS THE LOAD-BEARING DISTINCTION ────────────────────────────────────
 *
 * The three provider situations are genuinely different products, not three
 * skins on one, and flattening them is how a UI teaches a dangerous falsehood.
 * The one that matters most:
 *
 *   EXTERNAL SECRETS MUST NOT OFFER A CREATE BUTTON. forge's ExternalSecrets is
 *   an inert marker — forge never sees those values, renders nothing, and
 *   validates nothing; the operator provisions the k8s Secret out of band. A
 *   create button there would appear to write a value that in fact went
 *   nowhere, or worse, would imply reliant is the system of record for a secret
 *   it has never held. The correct affordance is a sentence explaining where
 *   the value actually comes from.
 *
 * ── WHY MODE IS NOT READ OFF forge's PROVIDER STRING ────────────────────────
 *
 * It cannot be, today. forge emits exactly three provider kinds — file,
 * external, none (internal/secrets/secrets.go) — and has no `managed` kind at
 * all. The managed store is a control-plane service that forge does not yet
 * know exists, so the only honest signal that it is in play is whether its RPCs
 * answer. Hence `storeAvailable` is an input here rather than something derived
 * from the report.
 *
 * When forge grows a `managed` provider kind, THIS is the function to change,
 * and the change is to prefer the report's own claim over the reachability
 * probe. Until then, reachability is the signal, and it is deliberately
 * subordinate to `external`: an environment that forge says is external stays
 * external even if a managed store happens to be reachable, because forge's
 * declaration is the thing that determines where the value is actually read
 * from at deploy time. Getting that precedence backwards would offer a write
 * into a store nothing reads.
 */

import type { ForgeSecretsReport } from "./secrets";
import { declarationsOf, providerKind, secretEntries } from "./secrets";
import type { ForgeSecretDeclaration } from "./secrets";
import type { ManagedSecretState, ManagedSecretSummary } from "./secretStore";
import { managedSecretState } from "./secretStore";

// ── Mode ────────────────────────────────────────────────────────────────────

/**
 * Which secret product this environment is running.
 *
 *   managed   the hosted store. Full versioned surface: create, set, delete,
 *             undelete, destroy, history.
 *   file      forge's gitignored YAML, dev/e2e only. Read-only HERE — the way
 *             you write one is `forge secret set` on your own machine, and
 *             offering a web form that cannot reach that file would be a
 *             button that lies.
 *   external  declared, provisioned out of band. No create, by design.
 *   none      no store configured at all.
 */
export type SecretSurfaceMode = "managed" | "file" | "external" | "none";

export function surfaceMode(
  report: ForgeSecretsReport | null | undefined,
  storeAvailable: boolean
): SecretSurfaceMode {
  const kind = providerKind(report);

  // External wins over reachability. See the header: forge's declaration is
  // what decides where the value is read at deploy time, so a reachable
  // managed store does not make an external environment writable.
  if (kind === "external") return "external";
  if (kind === "file") return "file";

  // `none` and `unknown` are where a managed store can legitimately be the
  // answer: forge has no provider of its own for this env, and the hosted
  // store is not something forge can currently name.
  return storeAvailable ? "managed" : "none";
}

/** Whether this mode can write a value from the browser at all. */
export function modeSupportsWrite(mode: SecretSurfaceMode): boolean {
  return mode === "managed";
}

export function modeLabel(mode: SecretSurfaceMode): string {
  switch (mode) {
    case "managed":
      return "Managed store";
    case "file":
      return "Local file store";
    case "external":
      return "External secret manager";
    default:
      return "No store configured";
  }
}

/**
 * One sentence on where values live and who puts them there. This is the text
 * that stands in for a create button when there is not going to be one, so it
 * has to answer "then how DO I set this" rather than just refusing.
 */
export function modeExplanation(mode: SecretSurfaceMode): string {
  switch (mode) {
    case "managed":
      return "Values live in the managed store and are fetched at deploy time over an authenticated channel. You can set them here; you cannot read them back.";
    case "file":
      return "Values live in a gitignored file on your own machine, for local development only. Set them with forge secret set — reliant cannot reach that file from here.";
    case "external":
      return "An external secret manager holds these values and they are provisioned out of band. Forge never sees them, so there is nothing for reliant to create or read.";
    default:
      return "This environment has no secret store configured, so nothing holds a value for it yet.";
  }
}

// ── The joined row ──────────────────────────────────────────────────────────

/**
 * What one row on the screen IS.
 *
 * `declared-unset` is the row this whole module exists to produce, and it is
 * distinct from `orphan` in both directions of failure:
 *
 *   declared-unset  a workload asks for it; the store has never held it. This
 *                   is the one that breaks a deploy, and it is the row a user
 *                   came to this screen to find.
 *   orphan          the store holds it; nothing declares it. Nothing injects
 *                   it anywhere, so it is dead weight rather than a failure —
 *                   usually a renamed env var or a removed workload.
 */
export type SecretRowOrigin = "declared" | "declared-unset" | "orphan";

export interface SecretSurfaceRow {
  name: string;
  origin: SecretRowOrigin;
  /** Present only when the managed store holds a record for this name. */
  summary: ManagedSecretSummary | null;
  /** Managed state, or null when there is no managed record to have a state. */
  state: ManagedSecretState | null;
  /** Which workloads ask for this. Coordinates, never contents. */
  declaredBy: ForgeSecretDeclaration[];
}

/**
 * Join forge's declarations with the managed store's records.
 *
 * Both sides are optional and the function is total: a screen that has forge's
 * answer but not the store's (or the reverse) still renders every row it can
 * justify, rather than waiting for both and showing nothing. That matters
 * because the two come from different backends with different failure modes,
 * and one being down should degrade the screen, not blank it.
 */
export function joinSecretRows(
  report: ForgeSecretsReport | null | undefined,
  managed: ManagedSecretSummary[] | null | undefined
): SecretSurfaceRow[] {
  const byName = new Map<string, ManagedSecretSummary>();
  for (const summary of managed ?? []) byName.set(summary.name, summary);

  const rows: SecretSurfaceRow[] = [];
  const seen = new Set<string>();

  // Declared secrets first — they are the ones with a consumer, and therefore
  // the ones whose absence is a problem rather than a curiosity.
  for (const entry of secretEntries(report)) {
    seen.add(entry.name);
    const summary = byName.get(entry.name) ?? null;
    rows.push({
      name: entry.name,
      origin: summary ? "declared" : "declared-unset",
      summary,
      state: summary ? managedSecretState(summary) : null,
      declaredBy: declarationsOf(entry),
    });
  }

  // Then whatever the store holds that nothing declares.
  for (const summary of managed ?? []) {
    if (seen.has(summary.name)) continue;
    rows.push({
      name: summary.name,
      origin: "orphan",
      summary,
      state: managedSecretState(summary),
      declaredBy: [],
    });
  }

  return rows.sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * The stat strip's numbers, derived from the ROWS rather than counted
 * independently.
 *
 * Same discipline as services/forge/secrets.ts's missingNames: a headline
 * number computed by a separate path can disagree with the rows underneath it,
 * and when it does, the number is what the reader believes. Deriving it from
 * the rendered rows makes that class of bug unrepresentable.
 */
export interface SecretSurfaceTally {
  total: number;
  set: number;
  /** Declared with no value anywhere. The number that means "this will not deploy". */
  unset: number;
  /** Soft-deleted or destroyed — present in history, not live. */
  inactive: number;
  orphans: number;
}

export function tallyRows(rows: SecretSurfaceRow[]): SecretSurfaceTally {
  const tally: SecretSurfaceTally = { total: rows.length, set: 0, unset: 0, inactive: 0, orphans: 0 };
  for (const row of rows) {
    if (row.origin === "orphan") tally.orphans += 1;
    if (row.origin === "declared-unset") {
      tally.unset += 1;
      continue;
    }
    if (row.state === "set") tally.set += 1;
    else if (row.state === "deleted" || row.state === "destroyed" || row.state === "tombstoned") {
      tally.inactive += 1;
    }
  }
  return tally;
}

// ── Row presentation vocabulary ─────────────────────────────────────────────

export function rowStatusLabel(row: SecretSurfaceRow): string {
  if (row.origin === "declared-unset") return "Not set";
  switch (row.state) {
    case "set":
      return "Set";
    case "deleted":
      return "Deleted";
    case "destroyed":
      return "Destroyed";
    case "tombstoned":
      return "No versions";
    default:
      return "Unknown";
  }
}

/**
 * The status dot's variant.
 *
 * `declared-unset` is the only `error`, and that is the point: it is the single
 * state on this screen that will actually stop a deploy. A soft-deleted secret
 * is a deliberate act with an undo, and painting it red next to a genuine
 * blocker would flatten the one distinction the reader needs.
 */
export function rowStatusVariant(
  row: SecretSurfaceRow
): "active" | "warning" | "error" | "neutral" {
  if (row.origin === "declared-unset") return "error";
  switch (row.state) {
    case "set":
      return "active";
    case "deleted":
    case "tombstoned":
      return "warning";
    case "destroyed":
      return "neutral";
    default:
      return "neutral";
  }
}
