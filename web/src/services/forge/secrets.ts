// Copyright (c) 2025 Reliant Labs

/**
 * Forge secrets data layer.
 *
 * Sibling of services/forge/topology.ts and the same shape: the JSON contract,
 * plus the pure projections the view renders. No React, no styling. Response
 * classification is NOT re-implemented here — `classifyForgeResponse` in
 * topology.ts already resolves is_forge_project / supported / reachability, and
 * the `Certainty` vocabulary is imported from there too, because presence has
 * exactly the same three-level problem that image state does.
 *
 * THE VALUE IS NEVER HERE. Every field below is a name, a boolean, or a
 * coordinate (workload / Secret name / Secret key). That is not an accident of
 * this file — forge's report type graph is pinned by an allow-list reflection
 * test, the daemon withholds forge's stderr entirely on the secret path rather
 * than risk echoing a value, and the RPC response is pinned by
 * TestListForgeSecretsResponseCannotCarrySecretValues. This module must not
 * widen that: it reads only the fields declared below, so a value that somehow
 * appeared in report_json would be dropped rather than displayed.
 *
 * PROVIDER KIND IS LOAD-BEARING, and it is the reason presence needs three
 * levels rather than two. `missing_count: 13` means "this environment cannot
 * start" when the provider is `file` — and means nothing at all when the
 * provider is `external`, because forge does not hold those values; an external
 * secret manager does, and forge never looked. Painting external as thirteen
 * missing secrets would invent an outage. `none` is a third case again: no store
 * is configured, so there is nothing that could have been checked.
 *
 * So presence maps to certainty like this:
 *
 *   present               known-good   the store has a value under this key
 *   missing  (file only)  known-bad    declared, and the store has no value
 *   not-held (external)   unknown      forge does not hold it; nobody looked
 *   undetermined          unknown      no store configured, or forge said nothing
 */

import type { Certainty } from "./topology";

// ── Forge's report document ─────────────────────────────────────────────────
//
// Typed against `forge secret list --json`. Like every forge report contract it
// is ADDITIVE, so everything forge marks omitempty is optional here and nothing
// is assumed non-null: a report from a newer or older forge must render rather
// than throw.

/**
 * Where a workload fetches a secret FROM. Coordinates, not contents: the
 * workload that declared the need, what kind of workload it is, and the
 * Kubernetes Secret name/key the value is read out of at inject time. Useful for
 * "who breaks if this is missing", and none of it is a value.
 */
export interface ForgeSecretDeclaration {
  workload?: string;
  kind?: string;
  secret_name?: string;
  secret_key?: string;
}

export interface ForgeSecretEntry {
  name: string;
  /** True when the store holds a value under this key. Absent means forge did not say. */
  present?: boolean;
  declared_by?: ForgeSecretDeclaration[];
}

export interface ForgeSecretsReport {
  env?: string;
  /** "file" | "none" | "external". An unrecognised kind is treated as unknown. */
  provider?: string;
  store_path?: string;
  /** Whether the store file exists AT ALL. Distinct from "exists and is empty". */
  store_exists?: boolean;
  secrets?: ForgeSecretEntry[];
  /** Keys present in the store that NO workload declares — nothing injects them. */
  inert?: string[];
  missing?: string[];
  missing_count?: number;
  ok?: boolean;
}

// ── Provider kind ───────────────────────────────────────────────────────────

/**
 * The provider kinds whose meaning this build understands, plus `unknown` for
 * anything else. The fallback direction matters for the same reason it does in
 * topology.ts: an unrecognised provider must not be assumed to be `file`, or a
 * newer forge would have its secrets rendered as a wall of false failures.
 */
export type SecretProviderKind = "file" | "external" | "none" | "unknown";

export function providerKind(report: ForgeSecretsReport | null | undefined): SecretProviderKind {
  switch (report?.provider) {
    case "file":
      return "file";
    case "external":
      return "external";
    case "none":
      return "none";
    default:
      return "unknown";
  }
}

/**
 * True only when forge itself holds the values, and therefore only when
 * present/missing is a claim forge is in a position to make.
 */
export function providerHoldsValues(kind: SecretProviderKind): boolean {
  return kind === "file";
}

export function providerLabel(kind: SecretProviderKind): string {
  switch (kind) {
    case "file":
      return "File store";
    case "external":
      return "External secret manager";
    case "none":
      return "No store configured";
    default:
      return "Unrecognised provider";
  }
}

/** One sentence on what the provider means for whether presence is knowable. */
export function providerExplanation(kind: SecretProviderKind): string {
  switch (kind) {
    case "file":
      return "Forge holds the values for this environment in a store file on disk, so it can say which declared secrets have a value and which do not.";
    case "external":
      return "An external secret manager holds the values. Forge does not have them and did not look, so it cannot say whether any of these are set — that is unknown here, not missing.";
    case "none":
      return "This environment has no secret store configured, so there is nothing for forge to have checked. Declared secrets below are listed for reference only.";
    default:
      return "This build of reliant does not recognise the provider forge reported, so presence is treated as unknown rather than guessed.";
  }
}

// ── Store state ─────────────────────────────────────────────────────────────

/**
 * What the store IS, before any per-secret question.
 *
 * `absent` and `empty` are deliberately different values. "No store file has
 * been created yet" and "a store exists and contains nothing" produce the same
 * thirteen missing secrets and call for different next actions — the first needs
 * the store created, the second means someone created it and the values never
 * landed, which is a partially-completed setup rather than an untouched one.
 * Collapsing them is exactly the kind of flattening the three-level vocabulary
 * exists to prevent.
 */
export type SecretStoreState =
  /** provider file, store_exists false: nothing on disk yet. */
  | "absent"
  /** provider file, the store exists and holds ZERO keys. */
  | "empty"
  /** provider file, the store exists and holds at least one key. */
  | "populated"
  /** provider external: the values are somewhere forge does not read. */
  | "external"
  /** provider none (or unrecognised): no store to describe. */
  | "unconfigured";

/**
 * storeKeyCount counts the keys the STORE holds, which is not the same as the
 * number of secrets declared. A store key is either declared by some workload
 * (and so appears as a present secret) or declared by none (and so appears in
 * `inert`). Summed from the report's own arrays rather than read from a count
 * field so it cannot disagree with the rows rendered beneath it.
 */
export function storeKeyCount(report: ForgeSecretsReport | null | undefined): number {
  const present = secretEntries(report).filter((entry) => entry.present === true).length;
  return present + inertKeys(report).length;
}

export function storeState(report: ForgeSecretsReport | null | undefined): SecretStoreState {
  const kind = providerKind(report);
  if (kind === "external") return "external";
  if (kind !== "file") return "unconfigured";
  if (report?.store_exists !== true) return "absent";
  return storeKeyCount(report) > 0 ? "populated" : "empty";
}

// ── Per-secret presence ─────────────────────────────────────────────────────

export type SecretPresence = "present" | "missing" | "not-held" | "undetermined";

const CERTAINTY_BY_PRESENCE: Record<SecretPresence, Certainty> = {
  present: "known-good",
  missing: "known-bad",
  // Both unknowns. `not-held` is "forge does not have this and never looked";
  // `undetermined` is "there was nothing to look at, or forge said nothing".
  // Different sentences, same certainty: not a pass and not a failure.
  "not-held": "unknown",
  undetermined: "unknown",
};

export function certaintyOfPresence(presence: SecretPresence): Certainty {
  return CERTAINTY_BY_PRESENCE[presence];
}

/**
 * presenceOf resolves one secret against the provider.
 *
 * The provider gate comes FIRST and overrides the entry's own `present` flag.
 * Under an external provider a `present: false` is not evidence of anything —
 * forge did not consult the external manager — so reporting it as missing would
 * turn a correctly-configured environment into thirteen fabricated failures.
 */
export function presenceOf(
  report: ForgeSecretsReport | null | undefined,
  entry: ForgeSecretEntry
): SecretPresence {
  const kind = providerKind(report);
  if (kind === "external") return "not-held";
  if (kind !== "file") return "undetermined";
  if (entry.present === true) return "present";
  if (entry.present === false) return "missing";
  return "undetermined";
}

export function presenceLabel(presence: SecretPresence): string {
  switch (presence) {
    case "present":
      return "Set";
    case "missing":
      return "Missing";
    case "not-held":
      return "Held externally";
    default:
      return "Not known";
  }
}

export function presenceExplanation(presence: SecretPresence): string {
  switch (presence) {
    case "present":
      return "The store holds a value under this key. Reliant never reads it — only that it is there.";
    case "missing":
      return "This secret is declared by a workload and the store has no value for it. forge secret ensure exits non-zero on this, so the environment cannot start.";
    case "not-held":
      return "An external secret manager owns this value. Forge does not hold it and did not check, so whether it is set is unknown — not missing.";
    default:
      return "Forge made no statement about this secret, so its presence is unknown.";
  }
}

// ── Projections ─────────────────────────────────────────────────────────────

export function secretEntries(report: ForgeSecretsReport | null | undefined): ForgeSecretEntry[] {
  if (!report || !Array.isArray(report.secrets)) return [];
  return report.secrets.filter(
    (entry): entry is ForgeSecretEntry => !!entry && typeof entry.name === "string"
  );
}

export function inertKeys(report: ForgeSecretsReport | null | undefined): string[] {
  if (!report || !Array.isArray(report.inert)) return [];
  return report.inert.filter((key): key is string => typeof key === "string" && key !== "");
}

export function declarationsOf(entry: ForgeSecretEntry): ForgeSecretDeclaration[] {
  if (!Array.isArray(entry.declared_by)) return [];
  return entry.declared_by.filter((decl): decl is ForgeSecretDeclaration => !!decl);
}

/**
 * missingNames is derived from the ROWS, not read from report.missing.
 *
 * Same discipline as topology's certaintyTally: the headline number must be the
 * count of rows the reader can see marked missing, or the two can disagree after
 * the provider gate has reclassified every row — which is precisely what happens
 * under an external provider, where forge's own `missing` array is populated and
 * the correct rendering is zero.
 */
export function missingNames(report: ForgeSecretsReport | null | undefined): string[] {
  return secretEntries(report)
    .filter((entry) => presenceOf(report, entry) === "missing")
    .map((entry) => entry.name);
}

/** presenceTally rolls the rows up into the three certainty categories. */
export function presenceTally(
  report: ForgeSecretsReport | null | undefined
): Record<Certainty, number> {
  const totals: Record<Certainty, number> = { "known-good": 0, "known-bad": 0, unknown: 0 };
  for (const entry of secretEntries(report)) {
    totals[certaintyOfPresence(presenceOf(report, entry))] += 1;
  }
  return totals;
}

/**
 * blocksEnvUp answers the question the screen exists for: would `forge secret
 * ensure` fail, and therefore would `forge env up` refuse to start this env?
 *
 * Only ever true under a `file` provider. `report.ok` is deliberately not used:
 * forge computes it without knowing that we decline to render external presence
 * as failure, so trusting it would reintroduce the false-outage rendering
 * through the back door.
 */
export function blocksEnvUp(report: ForgeSecretsReport | null | undefined): boolean {
  return missingNames(report).length > 0;
}

/** describeInert explains why a store key nothing declares is inert, not spare. */
export const INERT_EXPLANATION =
  "These keys exist in the store but no workload declares them, so forge injects them nowhere. " +
  "Nothing reads them at runtime. Usually this is a typo in a key name, a secret left behind by a " +
  "workload that has since been removed, or plain configuration that belongs in " +
  "deploy/kcl/<env>/config.k rather than in a secret store.";
