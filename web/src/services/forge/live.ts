// Copyright (c) 2025 Reliant Labs

/**
 * LIVE: what the control plane knows about a forge project's environments,
 * in ONE round trip, with NO DAEMON ANYWHERE ON THIS PATH.
 *
 * This module is the data half of the owner's rule (design §8.0, O-14):
 *
 *   anything the UI SHOWS or DOES goes straight to the control plane from the
 *   browser, with the user's session. Never through the daemon.
 *
 * The rule exists because of a specific failure. The env page used to call
 * `forge.env_status` on the daemon, and forge then called control-plane's
 * ListEnvironments with the DAEMON's token — so a page the user was entitled
 * to see came back 403, and an asleep laptop blanked a production environment
 * the control plane had been observing the whole time. Live now asks
 * DeployService.GetLiveView itself, over the session transport every other
 * cloud module uses (services/controlPlane/client.ts). A daemon that is
 * offline, slow, or has never existed changes nothing about what Live shows.
 *
 * The only daemon-dependent surface is PREVIEW, which renders the user's
 * checkout and genuinely cannot be done without their files, and BUILD. Both
 * live under the Preview tab.
 *
 * ── ONE CALL, NOT SIX ───────────────────────────────────────────────────────
 *
 * GetLiveView returns each environment with its declaration, current
 * promotion, release, bundle, latest apply and sessions together. That is
 * deliberate: the page cannot render until it has all of it, and N
 * environments × six reads is both a slow page and a page assembled from six
 * different instants. `declared_shape` lives on the environment row for the
 * same reason — so the default view of an env needs nothing but the control
 * plane.
 *
 * ── PLAIN SHAPES, READ EXPLICITLY ───────────────────────────────────────────
 *
 * Like cloudEnvs.ts, this converts the generated messages into plain
 * serialisable objects rather than passing them to the view. Two payoffs: the
 * view layer stays testable with object literals, and every field is read
 * EXPLICITLY — a field a newer control plane adds is dropped here rather than
 * spread into a component that did not expect it.
 *
 * ── INTENT IS PRIMARY; CONVERGENCE IS AN OBSERVATION ────────────────────────
 *
 * An environment's primary record is its INTENT: "this should run release
 * v12", written by a promotion. Whether it actually got there is a SECONDARY
 * reading, made by the control plane watching the reconciler converge the
 * cluster, and it is derived here as `observed`.
 *
 * Nothing on this path is a client's report. There used to be an "apply" — a
 * forge process announcing it was applying something and then saying how it
 * went — and every surface that showed one had to label it "reported by forge"
 * to stay honest. That model is gone: forge does not apply, so there is no
 * reporter and nothing to label. The same derivation serves every environment
 * kind, including one on a cluster the customer owns.
 *
 * ── ABSENCE IS NOT AGREEMENT ────────────────────────────────────────────────
 *
 * Three answers mean "we cannot say", and none of them may render as a
 * success:
 *
 *   not reported   no reading exists at all. The common case right now, and a
 *                  normal one — an environment nobody has observed yet.
 *   unknown        a reading exists and does not settle the question: it went
 *                  stale, or it was taken against an earlier promotion.
 *   converging     observed at a revision that is not the promoted one, with
 *                  no reported failure. The reconciler is still working.
 *
 * `observed` keeps them apart deliberately, and `provenance`-style collapsing
 * into one "ok / not ok" flag is exactly what it exists to prevent.
 *
 * ── WHAT IS DELIBERATELY NOT HERE ───────────────────────────────────────────
 *
 * `current_bundle` and `sessions` are not read. Per-object drift is not read
 * either, because the control plane does not compute it: the baseline it was
 * measured against disappeared with the apply model, so the list is always
 * empty and rendering it would imply a capability the system does not have.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import type { JsonObject } from "@bufbuild/protobuf";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  DeployEnvironmentKind,
  DeployRolloutPhase,
  type DeployConvergence,
  type DeployEnvironment,
  type DeployLiveEnvironment,
  type DeploySourceProvenance,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { CONTROL_PLANE_API_URL } from "@/services/controlPlane/config";

// ── Kinds ───────────────────────────────────────────────────────────────────

/**
 * What the control plane says an environment IS.
 *
 *   persistent    the platform runs its workloads (Reliant cloud).
 *   preview       the same, with an expiry (one per pull request).
 *   self_managed  forge applies it to a cluster the user owns. The platform
 *                 places nothing and converges nothing, but it IS the ledger
 *                 and its secrets are write-only. Shown as "Your cluster".
 *   local         runs on a developer machine via `forge env up`. The control
 *                 plane is its secret store and session register, nothing more.
 *   unknown       a kind this build does not recognise. NEVER folded into
 *                 persistent — that would offer cloud language (deployments,
 *                 rollouts) for an environment that may have neither.
 */
export type LiveEnvKind = "persistent" | "preview" | "self_managed" | "local" | "unknown";

export function liveKindLabel(kind: LiveEnvKind): string {
  switch (kind) {
    case "persistent":
      return "Reliant cloud";
    case "preview":
      return "Preview";
    case "self_managed":
      return "Your cluster";
    case "local":
      return "Local";
    default:
      return "Unknown";
  }
}

function kindOf(kind: DeployEnvironmentKind): LiveEnvKind {
  switch (kind) {
    case DeployEnvironmentKind.PERSISTENT:
      return "persistent";
    case DeployEnvironmentKind.PREVIEW:
      return "preview";
    case DeployEnvironmentKind.SELF_MANAGED:
      return "self_managed";
    case DeployEnvironmentKind.LOCAL:
      return "local";
    default:
      return "unknown";
  }
}

/**
 * True when the PLATFORM places this environment's workloads, and therefore
 * when a per-workload GetStatus observation means something. A self-managed
 * env's workloads run on a cluster the platform has no deployment rows for,
 * so asking would answer for something it never placed.
 *
 * THIS DOES NOT GATE THE CONVERGENCE DISPLAY, and that is the change. The
 * reconciler's status is per ENVIRONMENT and exists for every kind, so intent
 * versus observed is one answer for all of them — see LiveEnv.observed. The
 * only thing still per-kind is the WORKLOAD table, which reads a different
 * source.
 */
export function isPlacedKind(kind: LiveEnvKind): boolean {
  return kind === "persistent" || kind === "preview";
}

// ── Rollout phase ───────────────────────────────────────────────────────────

export type LivePhase =
  | "unspecified"
  | "pending"
  | "progressing"
  | "stabilizing"
  | "succeeded"
  | "degraded"
  | "superseded"
  | "unknown";

function phaseOf(phase: DeployRolloutPhase): LivePhase {
  switch (phase) {
    case DeployRolloutPhase.PENDING:
      return "pending";
    case DeployRolloutPhase.PROGRESSING:
      return "progressing";
    case DeployRolloutPhase.STABILIZING:
      return "stabilizing";
    case DeployRolloutPhase.SUCCEEDED:
      return "succeeded";
    case DeployRolloutPhase.DEGRADED:
      return "degraded";
    case DeployRolloutPhase.SUPERSEDED:
      return "superseded";
    case DeployRolloutPhase.UNKNOWN:
      return "unknown";
    default:
      return "unspecified";
  }
}

// ── Observed convergence, and drift ─────────────────────────────────────────

/**
 * WHAT THE RECONCILER WAS OBSERVED TO HAVE DONE about the current intent.
 *
 *   converged      every target cluster is running the promoted bundle.
 *   converging     observed at some other revision, with no reported failure.
 *                  The reconciler is mid-flight; the answer is "wait".
 *   failed         the reconciler reported a failure. The answer is
 *                  "investigate", and it carries the reconciler's own reason.
 *   unknown        we looked and cannot say: the newest reading went stale, or
 *                  it was taken against an earlier promotion.
 *   not-reported   no reading exists. THE COMMON CASE TODAY — the control
 *                  plane's observer is off by default — and a normal state,
 *                  not a fault.
 *
 * `unknown` and `not-reported` are both absences and are kept apart because
 * they call for different sentences: one is "our readings went stale", which
 * is a problem, and the other is "nothing has reported yet", which is Tuesday.
 * NEITHER may ever render as converged.
 */
export type LiveConvergenceState =
  | "converged"
  | "converging"
  | "failed"
  | "unknown"
  | "not-reported";

export interface LiveObserved {
  state: LiveConvergenceState;
  /** When the reading was made. Absent when there is no reading. */
  observedAt?: string;
}

/**
 * Whether what is RUNNING matches what the intent names — the control plane's
 * own word, passed through rather than recomputed.
 *
 * `not-reported` is this module's, not the server's: the server expresses
 * "nothing observed" by omitting the field entirely, and a client that read a
 * missing message as `in_sync` would turn every unobserved environment green.
 */
export type LiveDriftState = "in_sync" | "drifted" | "unknown" | "not-reported";

export interface LiveDrift {
  state: LiveDriftState;
  observedAt?: string;
}

function driftStateOf(state: string): LiveDriftState {
  switch (state) {
    case "in_sync":
    case "drifted":
    case "unknown":
      return state;
    // A state this build does not recognise is an absence of information, not
    // agreement. Same rule, one line up from the server.
    default:
      return "unknown";
  }
}

/**
 * Derive the observed reading from the drift verdict and the rollout phase,
 * which the control plane computes from ONE comparison — so they cannot
 * disagree, and this cannot invent a third answer.
 *
 * The phase is consulted for exactly one thing: splitting `drifted` into
 * "still working" and "tried and failed". The control plane makes that split
 * on whether any reading was a reconciler FAILURE as opposed to merely being
 * at the wrong revision, and the two need opposite responses from a reader.
 */
function observedOf(drift: LiveDrift, phase: LivePhase): LiveConvergenceState {
  switch (drift.state) {
    case "in_sync":
      return "converged";
    case "drifted":
      return phase === "degraded" ? "failed" : "converging";
    case "unknown":
      return "unknown";
    default:
      return "not-reported";
  }
}

/**
 * ONE OBSERVATION of one target cluster, as the control plane read it from the
 * reconciler. A row in the timeline.
 *
 * `reason` and `message` are the RECONCILER'S OWN WORDS, untranslated
 * (ReconciliationFailed, HealthCheckFailed, ArtifactFailed). Somebody chasing
 * a failed deploy needs the string they can search for, not a paraphrase of
 * it — this is the one place a technical string beats a friendly one.
 *
 * Rows are TRANSITIONS, not polls: the observer appends only when the answer
 * changed, so a timeline is short and every entry means something.
 */
export interface LiveConvergence {
  id: string;
  /** converged | failed. Only terminal readings are recorded. */
  state: "converged" | "failed" | "unknown";
  /** The reconciler's own reason. A failed record always carries one. */
  reason: string;
  message: string;
  /** Which target cluster this reading is about. */
  cluster: string;
  observedAt?: string;
}

function convergenceStateOf(state: string): LiveConvergence["state"] {
  // Anything else is a state this build does not know. Never folded into
  // converged — see LiveConvergenceState.
  return state === "converged" || state === "failed" ? state : "unknown";
}

// ── Provenance ──────────────────────────────────────────────────────────────

/**
 * Where something came from. Every field is a CLAIM the render made about
 * itself, stored as such — identity is never client-supplied (O-11), so
 * nothing here is verified and the UI says "says" rather than "is".
 */
export interface LiveProvenance {
  repo: string;
  commit: string;
  branch: string;
  tag: string;
  dirty: boolean;
  /** Tree hash: one identity for clean and dirty builds. "" means unhashed. */
  tree: string;
  forgeVersion: string;
  /** Which checkout produced it. Label is a branch, else a directory basename. */
  worktree: { key: string; label: string; hostId: string } | null;
}

function toProvenance(msg: DeploySourceProvenance | undefined): LiveProvenance | null {
  if (!msg) return null;
  const worktree = msg.worktree;
  return {
    repo: msg.repo,
    commit: msg.commit,
    branch: msg.branch,
    tag: msg.tag,
    dirty: msg.dirty === true,
    tree: msg.tree,
    forgeVersion: msg.forgeVersion,
    worktree: worktree
      ? { key: worktree.key, label: worktree.label, hostId: worktree.hostId }
      : null,
  };
}

/** A commit as it is shown: the first 7 hex characters, or "" when absent. */
export function shortCommit(commit: string): string {
  const trimmed = commit.trim();
  return trimmed === "" ? "" : trimmed.slice(0, 7);
}

/**
 * One source's half of the provenance line: "main@abc1234", with the claims
 * that matter appended — "unmerged" when the render was not from the main ref,
 * "dirty" when the tree had uncommitted changes.
 *
 * `unmerged` is derived from the BRANCH rather than from any ancestry check,
 * because nothing in Phase A verifies ancestry (that is #516). So it says what
 * the render claimed about itself and no more.
 */
export function describeSource(provenance: LiveProvenance | null): string {
  if (!provenance) return "";
  const ref = provenance.tag || provenance.branch || provenance.worktree?.label || "";
  const commit = shortCommit(provenance.commit);
  const head = ref && commit ? `${ref}@${commit}` : ref || commit;
  if (head === "") return "";
  const claims: string[] = [];
  // A tag or the main branch is the reviewable path; anything else is a
  // render from a branch that has not landed.
  if (provenance.branch !== "" && provenance.branch !== "main" && !provenance.tag) {
    claims.push("unmerged");
  }
  if (provenance.dirty) claims.push("dirty");
  return claims.length > 0 ? `${head}, ${claims.join(", ")}` : head;
}

/**
 * THE PROVENANCE LINE, design §2.1.
 *
 * Live provenance has TWO parts, and the line shows both when they differ:
 * the IMAGES come from the release's source, and the CONFIG comes from the
 * bundle's KCL source. "same images, new config" is a real deploy, and a
 * single source string could not express it.
 *
 *   v12 · images main@abc1234 · config feat-x@def5678, unmerged, dirty
 *
 * When the two agree there is nothing to contrast, so the line collapses to
 * one source — repeating identical halves would imply a distinction that is
 * not there:
 *
 *   v12 · main@abc1234
 *
 * In Phase A `config from` IS `declared_by` (the env's last recorded render),
 * because bundles are Phase B. That is a weaker claim than a bundle's own
 * provenance and the caller labels it as such; the shape of the line does not
 * change when bundles arrive.
 */
export function provenanceLine(args: {
  release: string;
  images: LiveProvenance | null;
  config: LiveProvenance | null;
}): string {
  const parts: string[] = [];
  if (args.release.trim() !== "") parts.push(args.release.trim());

  const images = describeSource(args.images);
  const config = describeSource(args.config);

  if (images !== "" && config !== "" && images !== config) {
    parts.push(`images ${images}`, `config ${config}`);
  } else {
    const single = images || config;
    if (single !== "") parts.push(single);
  }
  return parts.join(" · ");
}

// ── The declared shape ──────────────────────────────────────────────────────

/**
 * What the env's last render DECLARED — projected from the same render
 * `forge env render` prints, recorded by `forge env build` / `forge env
 * deploy` / Register.
 *
 * `secrets` carries NAMES AND PROVIDERS ONLY (F-13). There is no value field
 * to read, which is what lets the Live secrets section show
 * "declared but never set" rows with no daemon: the names come from here, and
 * whether each is set comes from the managed store.
 *
 * Decoded from a `google.protobuf.Struct`, so every field is checked rather
 * than cast. A shape from a newer forge with extra keys renders; a malformed
 * one degrades to the fields that did parse rather than throwing the page away.
 */
export interface LiveShapeWorkload {
  name: string;
  runtime: string;
  cluster: string;
}

export interface LiveShapeSecret {
  name: string;
  provider: string;
  /** Which workloads ask for it. Coordinates, never contents. */
  declaredBy: string[];
}

export interface LiveShape {
  kind: LiveEnvKind;
  workloads: LiveShapeWorkload[];
  secrets: LiveShapeSecret[];
  domains: string[];
  clusters: string[];
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function strings(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is string => typeof item === "string" && item !== "");
}

function objects(value: unknown): Record<string, unknown>[] {
  if (!Array.isArray(value)) return [];
  return value.filter(
    (item): item is Record<string, unknown> => !!item && typeof item === "object" && !Array.isArray(item)
  );
}

/**
 * Decode `DeployEnvironment.declared_shape`. The Struct's content is the
 * canonical JSON of forge's `release.Shape`, so keys are snake_case — that is
 * what a Struct carries verbatim and what protojson accepts.
 *
 * Returns null for an absent or empty shape, which is the NEVER-BUILT state
 * and a normal one. Null is never conflated with "declares nothing": an env
 * that genuinely declares no workloads still has a shape, with empty arrays.
 */
export function toLiveShape(declared: JsonObject | undefined): LiveShape | null {
  if (!declared || typeof declared !== "object" || Array.isArray(declared)) return null;
  const raw = declared as Record<string, unknown>;
  if (Object.keys(raw).length === 0) return null;

  return {
    kind: shapeKindOf(str(raw.kind)),
    workloads: objects(raw.workloads)
      .map((workload) => ({
        name: str(workload.name),
        runtime: str(workload.runtime),
        cluster: str(workload.cluster),
      }))
      .filter((workload) => workload.name !== ""),
    secrets: objects(raw.secrets)
      .map((secret) => ({
        name: str(secret.name),
        provider: str(secret.provider),
        declaredBy: strings(secret.declared_by),
      }))
      .filter((secret) => secret.name !== ""),
    domains: strings(raw.domains),
    clusters: strings(raw.clusters),
  };
}

/** forge's own kind string (release.EnvKind), which is a closed set. */
function shapeKindOf(kind: string): LiveEnvKind {
  switch (kind) {
    case "persistent":
    case "preview":
    case "self_managed":
    case "local":
      return kind;
    default:
      return "unknown";
  }
}

// ── One environment ─────────────────────────────────────────────────────────

/**
 * An environment's whole Live answer.
 *
 * ── THE THREE STATES THAT ARE NOT ERRORS ────────────────────────────────────
 *
 * Two of them are modelled here, and getting them apart is the point:
 *
 *   never built       no control-plane row at all. This type cannot represent
 *                     it, because there is nothing to represent — the env is
 *                     simply absent from the list, and the page says
 *                     "Not built yet."
 *   declared, not     a row with a declared shape and NO promotion. Someone
 *   built yet         registered it from Preview, or `forge env build` ran and
 *                     nothing has been promoted. Secrets can be set.
 *   deployed          a row with a promotion and a release.
 *
 * None of the three is styled as a failure, and `declaredNotBuilt` exists so
 * a component cannot accidentally render the middle one as the last with an
 * empty release.
 */
export interface LiveEnv {
  id: string;
  name: string;
  /** The forge project (forge.yaml `name`). */
  project: string;
  kind: LiveEnvKind;
  createdAt?: string;

  /** The last render recorded for this env, or null if nothing ever recorded one. */
  declaredShape: LiveShape | null;
  /** Where that render came from. A claim (O-11). */
  declaredBy: LiveProvenance | null;
  declaredAt?: string;

  /** The release bound now, or "" when nothing has been promoted. */
  release: string;
  /** Where the release's IMAGES came from. */
  releaseProvenance: LiveProvenance | null;
  promotedAt?: string;
  promotedByActor: string;
  promotedByUserId: string;

  /**
   * The rollout phase, for EVERY kind. It is derived from the same comparison
   * `observed` and `drift` are, so the three cannot contradict each other.
   */
  phase: LivePhase;

  /**
   * WHAT WAS OBSERVED about the current intent. The secondary half of the
   * state line, and the same derivation for a hosted environment and one on
   * the customer's own cluster.
   */
  observed: LiveObserved;

  /**
   * Whether running matches intended — the control plane's verdict, with its
   * one human line of detail.
   */
  drift: LiveDrift;
  /** The server's own sentence about the drift verdict. May be empty. */
  driftDetail: string;

  /**
   * The ready-made provenance line (§2.1). Computed here, so the header, the
   * overview row and the releases timeline cannot word it three ways.
   */
  provenance: string;
}

/** Declared by a render, and nothing promoted to it yet. A normal state. */
export function declaredNotBuilt(env: LiveEnv): boolean {
  return env.release.trim() === "" && env.declaredShape !== null;
}

/** A row exists but nothing has ever recorded a render or a promotion. */
export function neverBuilt(env: LiveEnv): boolean {
  return env.release.trim() === "" && env.declaredShape === null;
}

function toISO(ts: Timestamp | undefined): string | undefined {
  if (!ts) return undefined;
  try {
    const date = timestampDate(ts);
    // A zero timestamp converts to the epoch, and "1 Jan 1970" rendered as a
    // promotion time looks like real data. Nothing is better than that.
    if (!Number.isFinite(date.getTime()) || date.getTime() <= 0) return undefined;
    return date.toISOString();
  } catch {
    return undefined;
  }
}

export function toLiveEnv(msg: DeployLiveEnvironment): LiveEnv | null {
  const environment: DeployEnvironment | undefined = msg.environment;
  if (!environment || environment.id === "" || environment.name === "") return null;

  const kind = kindOf(environment.kind);
  const declaredShape = toLiveShape(environment.declaredShape);
  const declaredBy = toProvenance(environment.declaredBy);
  const promotion = msg.currentPromotion;
  const release = msg.currentRelease;

  // The release's own provenance is the better source for "where the images
  // came from" — it is the record the bytes were cut from. The promotion's
  // copy is the fallback for a ledger row imported without one.
  const releaseProvenance =
    toProvenance(release?.provenance) ?? toProvenance(promotion?.releaseProvenance);
  const releaseVersion = promotion?.releaseVersion ?? release?.version ?? "";

  // THE ABSENT DRIFT MESSAGE IS THE COMMON CASE, and it is a state rather
  // than a gap. The control plane omits the field entirely when no pass has
  // observed this environment — which is every environment today, because its
  // observer is off by default — so this reads the absence explicitly as
  // "not-reported" instead of defaulting a missing message to agreement.
  const driftMsg = msg.drift;
  const drift: LiveDrift = driftMsg
    ? { state: driftStateOf(driftMsg.state), observedAt: toISO(driftMsg.observedAt) }
    : { state: "not-reported" };

  // The phase is per ENVIRONMENT now, for every kind, and is not gated on
  // whether the platform places the workloads: it comes from the reconciler's
  // status, which exists wherever the bundle is applied. The previous guard
  // zeroed it for a self-managed env because the phase then came from
  // per-deployment rows that only a placed env has.
  const phase = phaseOf(msg.phase ?? DeployRolloutPhase.UNSPECIFIED);

  return {
    id: environment.id,
    name: environment.name,
    project: environment.project ?? "",
    kind,
    createdAt: toISO(environment.createdAt),

    declaredShape,
    declaredBy,
    declaredAt: toISO(environment.declaredAt),

    release: releaseVersion,
    releaseProvenance,
    promotedAt: toISO(promotion?.createdAt),
    promotedByActor: promotion?.promotedByActor ?? "",
    promotedByUserId: promotion?.promotedByUserId ?? "",

    phase,
    observed: { state: observedOf(drift, phase), observedAt: drift.observedAt },
    drift,
    driftDetail: driftMsg?.detail ?? "",

    provenance: provenanceLine({
      release: releaseVersion,
      images: releaseProvenance,
      // Phase A: config-from IS declared_by. Bundles (the real config source)
      // arrive in Phase B and replace this argument, not the line's shape.
      config: declaredBy,
    }),
  };
}

// ── The state line's words ──────────────────────────────────────────────────

/**
 * THE INTENT HALF: what this environment is SUPPOSED to be running, and who
 * said so.
 *
 * Intent is stated first and in the present tense because it is the primary
 * record — it is true the moment the promotion is written, whatever the
 * cluster is doing. Returns "" when nothing has been promoted; the caller
 * already has better copy for that (declared-not-built / never-built).
 */
export function intentLine(env: LiveEnv): string {
  if (env.release.trim() === "") return "";
  const by = env.promotedByActor || (env.promotedByUserId ? "a user" : "");
  if (by === "") return `Should be running ${env.release}`;
  return `Should be running ${env.release}, promoted by ${by}`;
}

/**
 * THE OBSERVED HALF, in the customer's nouns.
 *
 * Every arm is phrased as a reading rather than a report, because nothing here
 * is reported by anything any more — the platform watched the cluster and this
 * is what it saw. "Reconciler", "Flux", "Kustomization" and "bundle digest"
 * are all ours and stay out: what a customer can act on is whether their
 * release arrived, and if not, whether to wait or to look.
 *
 * NOT-REPORTED AND UNKNOWN BOTH SAY SO PLAINLY, and neither borrows a word
 * from the converged arm. That is the rule the whole model turns on: an
 * absence of information must never read as agreement.
 */
export function observedLine(observed: LiveObserved): string {
  switch (observed.state) {
    case "converged":
      return "Confirmed running";
    case "converging":
      return "Still rolling out";
    case "failed":
      return "Couldn't finish rolling out";
    case "unknown":
      return "Can't confirm what's running";
    default:
      return "Not confirmed yet";
  }
}

/** The drift verdict as a short phrase, or "" when there is nothing to say. */
export function driftLine(drift: LiveDrift): string {
  switch (drift.state) {
    case "in_sync":
      return "Matches what you asked for";
    case "drifted":
      return "Doesn't match what you asked for";
    case "unknown":
      return "Can't tell whether it matches";
    default:
      return "";
  }
}

/**
 * Whether the observed half should read as a PROBLEM.
 *
 * True only for an actual reported failure. Neither absence is a problem:
 * "nothing has confirmed this yet" is the normal state of every environment
 * today, and styling it as a fault would put a warning on every screen and
 * teach people to ignore the one that matters.
 */
export function observedIsFailure(observed: LiveObserved): boolean {
  return observed.state === "failed";
}

// ── Availability ────────────────────────────────────────────────────────────

/**
 * Whether the control plane can answer for this project at all.
 *
 * The same five answers as cloudEnvs.ts, deliberately — this is the same
 * transport asking the same service, and a second vocabulary for the same
 * failures would mean two sets of copy for one condition. Four of the five
 * are NOT errors: a user running Reliant without a control plane, or whose
 * role may not read deploy state, simply sees no environments, and painting
 * that red would put a banner in front of every local-only forge project.
 */
export type LiveAvailability =
  | "available"
  | "no-control-plane"
  | "no-access"
  | "not-configured"
  | "unreachable";

export function liveAvailabilityFromError(err: unknown): LiveAvailability {
  if (!CONTROL_PLANE_API_URL) return "no-control-plane";
  if (err instanceof ConnectError) {
    if (err.code === Code.PermissionDenied) return "no-access";
    if (err.code === Code.Unimplemented) return "not-configured";
  }
  return "unreachable";
}

/**
 * The control plane's own explanation, for display beside an unavailable
 * state. DeployService carries no secret material in any direction, so its
 * messages are safe to show.
 */
export function liveErrorDetail(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  if (err instanceof Error) return err.message;
  return "";
}

export function hasLiveControlPlane(): boolean {
  return !!CONTROL_PLANE_API_URL;
}

// ── The RPC ─────────────────────────────────────────────────────────────────

/**
 * Every environment the control plane holds for ONE forge project, with its
 * declaration and binding. The whole Live screen, in one call, with the
 * user's session.
 *
 * The response is filtered by project HERE as well as on the server, for
 * cloudEnvs.listProjectEnvironments's reason: a control plane older than the
 * project dimension would ignore the unknown request field and answer with
 * the whole organization's environments, which rendered under this project
 * would put another project's prod on this screen. An old server's rows carry
 * no project, so they match nothing.
 *
 * An empty list is an EMPTY LIST, never an error: a project nobody has
 * deployed yet is a normal state.
 */
export async function getLiveView(project: string): Promise<LiveEnv[]> {
  const res = await getControlPlaneClient(DeployService).getLiveView({ project });
  return (res.environments ?? [])
    .map(toLiveEnv)
    .filter((env): env is LiveEnv => env !== null && env.project === project)
    .sort((a, b) => a.name.localeCompare(b.name));
}

export function toLiveConvergence(msg: DeployConvergence): LiveConvergence {
  return {
    id: msg.id,
    state: convergenceStateOf(msg.state),
    reason: msg.reason,
    message: msg.message,
    cluster: msg.cluster,
    observedAt: toISO(msg.observedAt),
  };
}

/**
 * One environment's OBSERVATION TIMELINE, newest first.
 *
 * It replaces the apply list, and the replacement is the model change rather
 * than a rename. An apply was a forge process's claim that it was applying
 * something; forge does not apply, so what is worth listing is not a sequence
 * of attempts but a sequence of readings.
 *
 * An empty list is an EMPTY LIST, and today it is the normal answer: nothing
 * observes these environments yet. The timeline is derived from the
 * reconciler's current status and is safe to lose, so its absence is never an
 * error and never blocks the rest of Live.
 */
export async function listEnvironmentConvergences(
  environmentId: string,
  limit = 25
): Promise<LiveConvergence[]> {
  const res = await getControlPlaneClient(DeployService).listConvergences({ environmentId, limit });
  return (res.convergences ?? []).map(toLiveConvergence);
}
