/**
 * Forge topology data layer.
 *
 * This module owns EVERYTHING about forge's JSON reports: their shape, how a
 * response is classified into an outcome the UI can render, and — the part that
 * matters most — the mapping from an image state to one of three CERTAINTY
 * categories. No React, no styling. Presentation lives in components/Forge.
 *
 * THE THREE CATEGORIES ARE THE POINT OF THIS FILE.
 *
 * Forge reports six image states, and collapsing them into a green/red pair is
 * the specific defect this whole surface exists to prevent:
 *
 *   known-good   match
 *   known-bad    drift, missing
 *   unknown      not_verified, unreachable, untagged
 *
 * `not_verified` is the DEFAULT — it is what every cell says before anyone asks
 * for verification — so painting it as good claims a clean bill of health for an
 * environment nobody looked at. `forge env status dev` once reported all-green
 * for an hour while a workload was OOMKilled and crashlooping; that is exactly
 * this bug. `unreachable` must not read as bad either: a check that reports a
 * VPN blip as a release failure is a check that gets switched off in its first
 * week. And `untagged` means the workload runs by a MUTABLE TAG, so its bytes
 * cannot be proven either way — a third thing, not a soft pass.
 *
 * Forge's own decoder refuses to default an unrecognised state name for the same
 * reason (see envTopologyReport in forge's internal/cli/env_topology.go), so an
 * unknown state here lands in `unknown`, never in `known-good`.
 */

import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

// ── Image state and certainty ───────────────────────────────────────────────

/** The six states forge's topology report can put in an image cell. */
export type ForgeImageState =
  | "not_verified"
  | "match"
  | "drift"
  | "missing"
  | "untagged"
  | "unreachable";

/**
 * How much a cell's state actually tells you. Three values, deliberately — see
 * the module comment for why two is a bug rather than a simplification.
 */
export type Certainty = "known-good" | "known-bad" | "unknown";

const CERTAINTY_BY_STATE: Record<ForgeImageState, Certainty> = {
  match: "known-good",
  drift: "known-bad",
  missing: "known-bad",
  not_verified: "unknown",
  unreachable: "unknown",
  untagged: "unknown",
};

/**
 * certaintyOf classifies an image state. An unrecognised state — a newer forge
 * reporting something this build has never heard of — is `unknown`. That
 * fallback direction is load-bearing: guessing "good" for an unread state is the
 * one wrong answer, because it is indistinguishable from a verified pass.
 */
export function certaintyOf(state: string | undefined): Certainty {
  if (!state) return "unknown";
  return CERTAINTY_BY_STATE[state as ForgeImageState] ?? "unknown";
}

/** True when the state came from an actual observation of a cluster. */
export function isObserved(state: string | undefined): boolean {
  return state === "match" || state === "drift" || state === "missing" || state === "untagged";
}

/** Short human label for a state. Never collapses two states onto one word. */
export function stateLabel(state: string | undefined): string {
  switch (state) {
    case "match":
      return "Match";
    case "drift":
      return "Drift";
    case "missing":
      return "Missing";
    case "untagged":
      return "Untagged";
    case "unreachable":
      return "Unreachable";
    case "not_verified":
      return "Not checked";
    default:
      return "Unknown";
  }
}

/**
 * One-line explanation of what a state means, used in tooltips and the legend.
 * The `unknown` three each get their OWN sentence — they are not variations of
 * one idea, and a shared "couldn't check" blurb would hide why.
 */
export function stateExplanation(state: string | undefined): string {
  switch (state) {
    case "match":
      return "The digest running in the cluster is the digest this release froze. Proven.";
    case "drift":
      return "The cluster is running a different digest than this release declares.";
    case "missing":
      return "This release declares the image, but no workload in the cluster runs it.";
    case "untagged":
      return "The workload runs by a mutable tag, so its bytes cannot be proven either way — neither matched nor drifted.";
    case "unreachable":
      return "The cluster could not be read, so live state is unknown. This is not evidence of a problem with the release.";
    case "not_verified":
      return "Nobody has looked at the cluster yet. This is the declared digest only — not a statement about what is running.";
    default:
      return "This build of reliant does not recognise the state forge reported, so it is treated as unknown.";
  }
}

// ── Forge's report documents ────────────────────────────────────────────────
//
// Typed against forge's `env topology --json` and `env verify --json`
// contracts, which are explicitly ADDITIVE: fields are added, never renamed or
// repurposed. Every field that forge marks `omitempty` is optional here, and
// nothing is assumed non-null — a report from a newer or older forge must
// render, not throw.

export interface ForgeReleaseGit {
  commit?: string;
  tag?: string;
  /** A release cut from a tree with uncommitted changes: its bytes match no reviewable commit. */
  dirty?: boolean;
}

export interface ForgePromotionLag {
  latest_release?: string;
  current?: boolean;
  releases_behind?: number;
  behind_seconds?: number;
  /** Forge's own rendering of the gap ("71d13h"). Empty when it could not be computed. */
  behind?: string;
}

export interface ForgeTopologyImage {
  image: string;
  digest?: string;
  state?: string;
  /** The digest (or, for untagged, the tag reference) actually observed. Only under verify. */
  running?: string;
  detail?: string;
}

export interface ForgeTopologyEnv {
  env: string;
  /** deploy/kcl/<env>/ exists in THIS checkout. False is a real state, not a failure. */
  declared?: boolean;
  /** Ever promoted. False is normal — the env has declared nothing to be wrong about. */
  bound?: boolean;
  release?: string;
  /** RFC3339 PROMOTE time. Promotion writes a pointer; deployment moves bytes. */
  promoted_at?: string;
  release_known?: boolean;
  git?: ForgeReleaseGit;
  release_created_at?: string;
  kube_context?: string;
  namespace?: string;
  lag?: ForgePromotionLag;
  images?: ForgeTopologyImage[];
  /** Forge's explanation of a state that would otherwise look like missing data. */
  note?: string;
}

export interface ForgeTopologyTally {
  not_verified?: number;
  match?: number;
  drift?: number;
  missing?: number;
  untagged?: number;
  unreachable?: number;
}

export interface ForgeTopologyReport {
  project?: string;
  latest_release?: string;
  releases?: string[];
  /** The UNION of image names across every environment — not every env has every image. */
  images?: string[];
  environments?: ForgeTopologyEnv[];
  verified?: boolean;
  tally?: ForgeTopologyTally;
  ok?: boolean;
}

/** `forge env verify --json` — one environment, freshly observed. */
export interface ForgeVerifyImage {
  image: string;
  declared?: string;
  running?: string;
  workloads?: string[];
  state?: string;
  detail?: string;
}

export interface ForgeVerifyReport {
  env?: string;
  bound?: boolean;
  release?: string;
  promoted_at?: string;
  kube_context?: string;
  namespace?: string;
  images?: ForgeVerifyImage[];
  tally?: ForgeTopologyTally;
  ok?: boolean;
  detail?: string;
}

// ── Response classification ─────────────────────────────────────────────────

/**
 * The distinct things a forge RPC can mean. Every one of these is a SUCCESSFUL
 * RPC — the backend deliberately returns "not a forge project", "your forge is
 * too old" and "the cluster was unreachable" as data, so that none of them can
 * be mistaken for the others or for a transport failure.
 */
export type ForgeOutcome<T> =
  /** No forge.yaml at the project path. Normal: most reliant projects are not forge projects. */
  | { kind: "not-forge-project"; meta: ForgeReportMeta }
  /** The forge reliant carries lacks the command or flag. Show the version, not an empty screen. */
  | { kind: "unsupported"; meta: ForgeReportMeta }
  /** The cluster could not be read and no report was produced at all. State is unknown. */
  | { kind: "unreachable"; meta: ForgeReportMeta }
  /** Forge produced a document we could not parse. Distinct from every state above. */
  | { kind: "malformed"; meta: ForgeReportMeta; raw: string }
  /** A report. `reachability` still matters: exit 2 can arrive WITH a partial report. */
  | { kind: "report"; meta: ForgeReportMeta; report: T };

/**
 * classifyForgeResponse turns a {meta, report_json} pair into an outcome.
 *
 * Order matters. `is_forge_project` and `supported` are checked before the body
 * because both guarantee an empty body — treating either as "malformed JSON"
 * would replace a precise, actionable message with a generic parse complaint.
 * A non-empty body wins over UNREACHABLE, because forge exiting 2 with a
 * partial report still tells the user which environments it did read; the
 * per-image `unreachable` states carry the uncertainty from there.
 */
export function classifyForgeResponse<T>(
  meta: ForgeReportMeta | undefined,
  reportJson: string | undefined
): ForgeOutcome<T> {
  const resolved: ForgeReportMeta =
    meta ??
    ({
      isForgeProject: false,
      supported: true,
      forgeVersion: "",
      unsupportedReason: "",
      exitCode: 0,
      reachability: ForgeReachability.UNSPECIFIED,
      unreachableReason: "",
    } as ForgeReportMeta);

  if (!resolved.isForgeProject) return { kind: "not-forge-project", meta: resolved };
  if (!resolved.supported) return { kind: "unsupported", meta: resolved };

  const raw = (reportJson ?? "").trim();
  if (raw === "") {
    if (resolved.reachability === ForgeReachability.UNREACHABLE) {
      return { kind: "unreachable", meta: resolved };
    }
    return { kind: "malformed", meta: resolved, raw: "" };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { kind: "malformed", meta: resolved, raw };
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return { kind: "malformed", meta: resolved, raw };
  }
  return { kind: "report", meta: resolved, report: parsed as T };
}

// ── Matrix projection ───────────────────────────────────────────────────────

/**
 * One cell of the environments × images matrix.
 *
 * `absent` is a FOURTH kind, not a seventh state, and keeping it out of the
 * state space is deliberate. The report's `images` array is the union across
 * every environment, so a cell can exist in the grid for an image that this
 * env's release simply does not contain. Rendering that as a blank invites the
 * reader to see zero, missing, or "not checked" — three claims forge never
 * made. Forge's own text renderer prints a middot for it and says so in the
 * header; we carry the same distinction structurally.
 */
export type TopologyCell =
  | { kind: "absent"; image: string }
  | {
      kind: "state";
      image: string;
      state: ForgeImageState | string;
      certainty: Certainty;
      digest?: string;
      running?: string;
      detail?: string;
    };

/** imageNames returns the union of image names, falling back to whatever the envs declare. */
export function imageNames(report: ForgeTopologyReport | null | undefined): string[] {
  if (!report) return [];
  if (Array.isArray(report.images) && report.images.length > 0) {
    return report.images.filter((name): name is string => typeof name === "string");
  }
  // A report without the union field (or with an empty one) still has per-env
  // images. Deriving the union here means the matrix renders rather than
  // collapsing to zero columns.
  const seen = new Set<string>();
  for (const env of report.environments ?? []) {
    for (const img of env.images ?? []) {
      if (img?.image) seen.add(img.image);
    }
  }
  return [...seen].sort();
}

/** environments returns the env rows, tolerating a report with none. */
export function environments(report: ForgeTopologyReport | null | undefined): ForgeTopologyEnv[] {
  if (!report || !Array.isArray(report.environments)) return [];
  return report.environments.filter((env): env is ForgeTopologyEnv => !!env && typeof env.env === "string");
}

/** cellFor resolves one env/image intersection. */
export function cellFor(env: ForgeTopologyEnv, image: string): TopologyCell {
  const found = (env.images ?? []).find((candidate) => candidate?.image === image);
  if (!found) return { kind: "absent", image };
  const state = found.state ?? "not_verified";
  return {
    kind: "state",
    image,
    state,
    certainty: certaintyOf(state),
    digest: found.digest,
    running: found.running,
    detail: found.detail,
  };
}

/**
 * mergeVerifyIntoTopology replaces one environment's image states with a fresh
 * `env verify` observation, leaving every other environment untouched.
 *
 * This is what makes "paint from the ledger, verify asynchronously" work: the
 * ledger view arrives from a fast local file read, and each env is upgraded from
 * `not_verified` to an observed state one at a time, without a 110-second
 * all-envs call blocking the screen.
 *
 * An image the verify report does not mention keeps its previous cell rather
 * than being dropped or zeroed — a verify that could not see an image has not
 * established that the release stopped declaring it.
 */
export function mergeVerifyIntoTopology(
  report: ForgeTopologyReport,
  envName: string,
  verify: ForgeVerifyReport
): ForgeTopologyReport {
  const byImage = new Map<string, ForgeVerifyImage>();
  for (const img of verify.images ?? []) {
    if (img?.image) byImage.set(img.image, img);
  }

  const environmentsNext = (report.environments ?? []).map((env) => {
    if (env.env !== envName) return env;
    const images = (env.images ?? []).map((img) => {
      const observed = byImage.get(img.image);
      if (!observed) return img;
      return {
        ...img,
        // Forge's verify states are the same vocabulary minus not_verified, so
        // an observation only ever moves a cell OUT of unknown-by-default.
        state: observed.state ?? img.state,
        running: observed.running,
        detail: observed.detail,
      };
    });
    return { ...env, images };
  });

  const next: ForgeTopologyReport = { ...report, environments: environmentsNext };
  next.tally = tallyOf(next);
  return next;
}

/** tallyOf recomputes the whole-screen counts from the matrix. */
export function tallyOf(report: ForgeTopologyReport): ForgeTopologyTally {
  const tally: Required<ForgeTopologyTally> = {
    not_verified: 0,
    match: 0,
    drift: 0,
    missing: 0,
    untagged: 0,
    unreachable: 0,
  };
  for (const env of report.environments ?? []) {
    for (const img of env.images ?? []) {
      const state = (img.state ?? "not_verified") as ForgeImageState;
      if (state in tally) tally[state] += 1;
      else tally.not_verified += 1;
    }
  }
  return tally;
}

/**
 * certaintyTally rolls the six counts up into the three categories the header
 * badges. Summed from the matrix rather than from `report.tally` so it cannot
 * disagree with what the cells render.
 */
export function certaintyTally(report: ForgeTopologyReport | null | undefined): Record<Certainty, number> {
  const totals: Record<Certainty, number> = { "known-good": 0, "known-bad": 0, unknown: 0 };
  if (!report) return totals;
  for (const env of report.environments ?? []) {
    for (const img of env.images ?? []) {
      totals[certaintyOf(img.state ?? "not_verified")] += 1;
    }
  }
  return totals;
}

// ── Env-level presentation facts (still pure) ───────────────────────────────

/**
 * envBindingState names what an environment row IS, before any verification.
 * These are three genuinely different situations and forge reports them
 * separately; flattening any of them into "broken" would be wrong.
 */
export type EnvBindingState =
  /** Bound and declared here: a normal row with a release and a cluster. */
  | "active"
  /** Never promoted. Normal — it has declared nothing to be wrong about. */
  | "unbound"
  /** Bound in the ledger, but no deploy/kcl/<env>/ in this checkout. */
  | "undeclared";

export function envBindingState(env: ForgeTopologyEnv): EnvBindingState {
  if (env.bound === false) return "unbound";
  if (env.declared === false) return "undeclared";
  return "active";
}

/** True when the bound release was cut from a tree with uncommitted changes. */
export function isDirtyRelease(env: ForgeTopologyEnv): boolean {
  return env.git?.dirty === true;
}

/** describeLag renders the two lag units forge reports, which answer different questions. */
export function describeLag(lag: ForgePromotionLag | undefined): string | null {
  if (!lag) return null;
  if (lag.current) return "On the latest release";
  const parts: string[] = [];
  const behind = lag.releases_behind ?? 0;
  if (behind > 0) parts.push(`${behind} release${behind === 1 ? "" : "s"} behind`);
  // `behind` empty means the gap could not be computed — an unknown gap and a
  // zero gap are different claims, so nothing is substituted for it.
  if (lag.behind) parts.push(lag.behind);
  return parts.length > 0 ? parts.join(" · ") : null;
}

/** shortDigest trims a sha256 digest for display without implying it was truncated by forge. */
export function shortDigest(digest: string | undefined): string {
  if (!digest) return "";
  const bare = digest.startsWith("sha256:") ? digest.slice("sha256:".length) : digest;
  return bare.length > 12 ? bare.slice(0, 12) : bare;
}
