// Copyright (c) 2025 Reliant Labs

/**
 * WHAT THIS CHECKOUT WOULD CHANGE, PER ENVIRONMENT.
 *
 * Forge renders the environments declared in a checkout and compares each one
 * against what is deployed. The document is forge's and is rendered as-is: the
 * comparison belongs to forge, and a second implementation here would be a copy
 * that eventually disagrees with the thing that actually decides.
 *
 * THE STATUS IS NOT OPTIONAL READING. Every environment carries one, and four
 * of the five values mean "this diff is not an answer". An environment whose
 * render failed, or which has no recorded configuration to compare against,
 * reports that — and an empty diff and an unanswerable one mean opposite
 * things. Treating a failure as "nothing changed" is the single mistake this
 * module's shape exists to prevent, which is why diffIsAnswered is separate
 * from reading the counts.
 *
 * ── THE FIELD NAMES ARE FORGE'S, MEASURED, NOT GUESSED ──────────────────────
 *
 * Every field below was read off real `forge env diff --all --json` output from
 * the pinned forge, captured into __fixtures__ and asserted against by the
 * tests. That is deliberate, because this module's first version was written
 * against invented names — `objects_added`, `objects_changed`, `images_changed`,
 * `secrets_needed` — and forge emits NONE of them. It emits `added`, `removed`
 * and `changed` as ARRAYS OF OBJECTS, and `secrets_added` as objects rather
 * than strings.
 *
 * The consequence was not a blank card. It was the exact failure the paragraph
 * above claims to prevent: a diff with nine added objects has no key any of
 * those count fields names, so every count read `undefined`, every list read
 * empty, and `diffHasChanges` returned FALSE — rendering a real change set as
 * "nothing would change" to the person about to deploy it. A type that cannot
 * be checked against the producer is not a contract, which is why the fixtures
 * are committed and the tests parse them rather than hand-building entries.
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

/** One rendered Kubernetes object, as forge identifies it. */
export interface ForgeShapeObject {
  cluster?: string;
  api_version?: string;
  kind?: string;
  namespace?: string;
  name?: string;
  /** The forge workload it belongs to — the unit a person thinks in. */
  workload?: string;
  hash?: string;
  /**
   * The same document with release-bound image references normalized to their
   * artifact keys. Equal config_hash with a different hash means ONLY the
   * images moved, which is what separates "config changed" from "would run a
   * new build".
   */
  config_hash?: string;
}

/** One object present on both sides whose hash differs. */
export interface ForgeObjectChange {
  live?: ForgeShapeObject;
  candidate?: ForgeShapeObject;
  /**
   * The non-image half changed. Forge sets this conservatively: when either
   * side lacks a config_hash the split cannot be made, and it reports true
   * whenever the hashes differ at all.
   */
  config_changed?: boolean;
  images?: ForgeImageChange[];
}

/** One artifact's pinned digest moving. */
export interface ForgeImageChange {
  artifact?: string;
  from?: string;
  to?: string;
}

/** A workload moving between runtimes or clusters. */
export interface ForgeRuntimeChange {
  workload?: string;
  from?: string;
  to?: string;
  from_cluster?: string;
  to_cluster?: string;
}

/** One declared secret: a NAME and a provider, never a value. */
export interface ForgeShapeSecret {
  name?: string;
  /** forge's provider discriminator: file | hosted | external | … */
  provider?: string;
  /** The workloads that read it. */
  declared_by?: string[];
}

/** A disagreement about an environment's kind. A kind is immutable. */
export interface ForgeKindChange {
  live?: string;
  candidate?: string;
}

/**
 * Forge's shape diff, field for field.
 *
 * Carried as-is. Nothing here is renamed on the way in: a reader comparing
 * this against `forge env diff --json` must be able to do it by eye, because
 * that is the only check that catches the two drifting apart.
 */
export interface ForgeShapeDiff {
  /**
   * There was no live shape to compare against. Every candidate object then
   * appears in `added` — which is why this flag exists and must never be
   * rendered as a change set. See diffIsFirstRender.
   */
  live_unknown?: boolean;
  /** Always an error for a reader to surface, never a deployable change. */
  kind_changed?: ForgeKindChange;

  added?: ForgeShapeObject[];
  removed?: ForgeShapeObject[];
  changed?: ForgeObjectChange[];

  workloads_added?: string[];
  workloads_removed?: string[];
  runtime_changes?: ForgeRuntimeChange[];
  secrets_added?: ForgeShapeSecret[];
  secrets_removed?: ForgeShapeSecret[];
  domains_added?: string[];
  domains_removed?: string[];
  clusters_added?: string[];
  clusters_removed?: string[];
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
   * What the live side was read from: "bundle", "declared_shape" or "none". A
   * diff against a recorded BUNDLE is stronger evidence than one against a
   * merely declared shape, and forge says which so a reader can weigh it.
   */
  live_source?: string;
  /** The render normalized to the deployed bundle's config digest. */
  config_identical?: boolean;
  /**
   * Per-secret presence on the live side. A name ABSENT from this map is
   * UNVERIFIABLE, never missing — forge cannot query an external provider's
   * store, and reporting those as missing would tell a user to set a secret
   * that is already fine.
   */
  secret_presence?: Record<string, boolean>;
}

export interface ForgeEnvDiffReport {
  project?: string;
  /** Which checkout was rendered. */
  source?: string;
  /** What it was compared against. */
  against?: string;
  /**
   * Whether Helm charts were templated. Off by default, and reported because a
   * diff that skipped charts means something different from one that compared
   * them.
   */
  charts?: boolean;
  environments?: ForgeEnvDiffEntry[];
}

/** The entries, in forge's order. */
export function diffEntries(
  report: ForgeEnvDiffReport | null | undefined
): ForgeEnvDiffEntry[] {
  return report?.environments ?? [];
}

/** One environment's entry, by name. */
export function diffEntryFor(
  report: ForgeEnvDiffReport | null | undefined,
  env: string
): ForgeEnvDiffEntry | null {
  return diffEntries(report).find((entry) => entry.env === env) ?? null;
}

/**
 * THE FIRST RENDER OF AN ENVIRONMENT NOTHING HAS EVER DEPLOYED.
 *
 * Forge is explicit that this is reported as "no recorded config" and NEVER as
 * "everything is added", and the reason is a misreading with real consequences:
 * `added` holds every object in the render, so a card showing "149 added" in
 * the place it normally shows changes invites someone to read a routine first
 * render as 149 pending changes.
 *
 * Both signals are checked because they answer different questions and forge
 * sets them independently. `live_source: "none"` means nothing was recorded to
 * compare against; `live_unknown` is the diff's own statement that it had no
 * live side. An entry carrying either one is not a comparison.
 */
export function diffIsFirstRender(entry: ForgeEnvDiffEntry): boolean {
  if (!diffIsAnswered(entry)) return false;
  return entry.live_source === "none" || entry.diff?.live_unknown === true;
}

/**
 * Whether an answered diff found any difference at all.
 *
 * Returns false for an UNANSWERED entry too, which is why it must never be the
 * only thing a caller checks — "no changes found" and "we could not look" both
 * come back false here, and only diffIsAnswered separates them. The pairing is
 * deliberate: a single boolean conflating them is the bug.
 *
 * A FIRST RENDER COUNTS AS CHANGES, because deploying it really would create
 * all of it. What a caller must not do is present those objects as a
 * comparison — diffIsFirstRender is how it tells the difference.
 */
export function diffHasChanges(entry: ForgeEnvDiffEntry): boolean {
  if (!diffIsAnswered(entry)) return false;
  return diffCategories(entry).length > 0 || entry.diff?.kind_changed != null;
}

/**
 * One row of the §8.2 grouping: a category, how many, and what it is called.
 *
 * Counts come from the LENGTH of forge's arrays rather than from a count field,
 * because forge publishes no count fields — the arrays are the data. Deriving
 * the number from the thing being listed also means a card's count and its
 * expanded contents cannot disagree.
 */
export interface EnvDiffCategory {
  key: EnvDiffCategoryKey;
  /** The §8.2 category heading, as a person reads it. */
  label: string;
  count: number;
  /** One line per item, already rendered to text. */
  items: string[];
}

export type EnvDiffCategoryKey =
  | "objects_added"
  | "objects_removed"
  | "objects_changed"
  | "workloads_added"
  | "workloads_removed"
  | "runtime_changes"
  | "secrets_added"
  | "secrets_removed"
  | "domains_added"
  | "domains_removed"
  | "clusters_added"
  | "clusters_removed";

/**
 * Forge's §8.2 categories for one entry, in forge's own order, EMPTY ONES
 * OMITTED.
 *
 * Omitted rather than listed as zero. A card that prints "0 removed, 0 domains
 * added, 0 clusters removed" makes the reader scan twelve rows to find the two
 * that happened, and an exhaustive list of zeroes reads as a checklist someone
 * has to verify. What changed is the information.
 *
 * Returns nothing at all for an unanswered entry, so a caller cannot
 * accidentally render a failed render's leftovers as categories.
 */
export function diffCategories(entry: ForgeEnvDiffEntry): EnvDiffCategory[] {
  if (!diffIsAnswered(entry)) return [];
  const diff = entry.diff;
  if (!diff) return [];

  const categories: EnvDiffCategory[] = [
    category("objects_added", "Objects added", (diff.added ?? []).map(describeObject)),
    category("objects_removed", "Objects removed", (diff.removed ?? []).map(describeObject)),
    category(
      "objects_changed",
      "Objects changed",
      (diff.changed ?? []).map(describeObjectChange)
    ),
    category("workloads_added", "Workloads added", diff.workloads_added ?? []),
    category("workloads_removed", "Workloads removed", diff.workloads_removed ?? []),
    category(
      "runtime_changes",
      "Workloads moved",
      (diff.runtime_changes ?? []).map(describeRuntimeChange)
    ),
    category(
      "secrets_added",
      "Secrets needed",
      (diff.secrets_added ?? []).map((secret) => describeSecretNeed(secret, entry.secret_presence))
    ),
    category(
      "secrets_removed",
      "Secrets no longer declared",
      (diff.secrets_removed ?? []).map(
        (secret) => `${secret.name ?? "(unnamed)"} — the value is not deleted`
      )
    ),
    category("domains_added", "Domains added", diff.domains_added ?? []),
    category("domains_removed", "Domains removed", diff.domains_removed ?? []),
    category("clusters_added", "Clusters added", diff.clusters_added ?? []),
    category("clusters_removed", "Clusters removed", diff.clusters_removed ?? []),
  ];
  return categories.filter((entry) => entry.count > 0);
}

function category(key: EnvDiffCategoryKey, label: string, items: string[]): EnvDiffCategory {
  return { key, label, count: items.length, items };
}

/** "Deployment api in ns" — forge's identity, as a sentence. */
export function describeObject(object: ForgeShapeObject): string {
  const name = [object.kind, object.name].filter(Boolean).join(" ") || "(unnamed object)";
  return object.namespace ? `${name} in ${object.namespace}` : name;
}

/**
 * A changed object, SAYING WHICH HALF MOVED.
 *
 * "would run a new build" and "config changed" ask different things of the
 * reader — one is a rebuild, the other is a settings change — and forge splits
 * them precisely so a card does not have to flatten them back together.
 */
export function describeObjectChange(change: ForgeObjectChange): string {
  const object = change.candidate ?? change.live ?? {};
  const images = (change.images ?? []).length > 0;
  const config = change.config_changed === true;
  const what = images && config
    ? "config changed, and would run a new build"
    : images
      ? "would run a new build"
      : "config changed";
  return `${describeObject(object)} — ${what}`;
}

/** "web: hosted on c1 → bucket" — the sentence a user needs. */
export function describeRuntimeChange(change: ForgeRuntimeChange): string {
  const where = (runtime?: string, cluster?: string) => {
    const base = runtime || "(none)";
    return cluster ? `${base} on ${cluster}` : base;
  };
  const workload = change.workload || "(unnamed workload)";
  return `${workload}: ${where(change.from, change.from_cluster)} → ${where(change.to, change.to_cluster)}`;
}

/**
 * A newly declared secret, joined with what is known about whether it is set.
 *
 * ABSENT FROM PRESENCE MEANS UNVERIFIABLE, NOT MISSING. An environment whose
 * secrets live with an external provider cannot be queried at all, and saying
 * "missing" there puts a false instruction in front of someone whose setup is
 * already correct. The three readings are distinct on purpose.
 */
export function describeSecretNeed(
  secret: ForgeShapeSecret,
  presence: Record<string, boolean> | undefined
): string {
  const name = secret.name ?? "(unnamed)";
  const label = secret.provider ? `${name} [${secret.provider}]` : name;
  const known = presence && Object.prototype.hasOwnProperty.call(presence, name);
  if (!known) return `${label} — declared; presence not verifiable`;
  return presence?.[name] ? `${label} — already set` : `${label} — not set yet`;
}

/** The secret keys this checkout needs that are not present on the live side. */
export function missingSecrets(entry: ForgeEnvDiffEntry): string[] {
  const presence = entry.secret_presence ?? {};
  return Object.entries(presence)
    .filter(([, present]) => present === false)
    .map(([key]) => key)
    .sort();
}

/**
 * The one line to show for an entry whose diff is not an answer.
 *
 * Forge's own `detail` when there is one, because forge knows why and a
 * paraphrase here would be a worse version of the same sentence. The fallbacks
 * are per status and never generic: "could not be determined" tells the reader
 * nothing they did not already see.
 *
 * NONE OF THESE IS AN ERROR, and the copy carries that. A failed render means
 * Preview could not look — it says nothing about the environment, which Live is
 * showing correctly on the next tab.
 */
export function unavailableReason(entry: ForgeEnvDiffEntry): string {
  const detail = entry.detail?.trim();
  if (detail) return detail;
  switch (envDiffStatusOf(entry.status)) {
    case "impure":
      return "Rendering this environment wrote files, so the result was discarded.";
    case "stale":
      return "Your checkout changed while rendering. Try again once your edits have settled.";
    case "error":
      return "This environment could not be rendered, so there is nothing to compare.";
    case "unsupported":
      return "This version of forge cannot render, so Preview has nothing to compare.";
    default:
      return "This environment's diff could not be read.";
  }
}
