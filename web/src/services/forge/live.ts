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
 * ── WHAT IS DELIBERATELY NOT HERE ───────────────────────────────────────────
 *
 * Phase A fills the environment, its declaration, the promotion and the
 * release. `current_bundle`, `latest_apply`, `sessions` and `drift` are Phase
 * B, and this module does not pretend to read them: a half-decoded apply
 * rendered as "succeeded" would be worse than an absent section.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import type { JsonObject } from "@bufbuild/protobuf";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  DeployEnvironmentKind,
  DeployRolloutPhase,
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
 * when a rollout phase and a GetStatus observation mean something. A
 * self-managed env is promoted but not converged, so asking the platform what
 * is running there would answer for a cluster it has never connected to.
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
  | "superseded";

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
    default:
      return "unspecified";
  }
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

  /** The platform's rollout phase. Always `unspecified` for a non-placed kind. */
  phase: LivePhase;

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

    // A phase only means something where the platform places workloads. For a
    // self-managed env the server sends UNSPECIFIED in Phase A, and this
    // guard makes a future server that sends something else harmless rather
    // than letting it claim a convergence nobody observed.
    phase: isPlacedKind(kind) ? phaseOf(msg.phase ?? DeployRolloutPhase.UNSPECIFIED) : "unspecified",

    provenance: provenanceLine({
      release: releaseVersion,
      images: releaseProvenance,
      // Phase A: config-from IS declared_by. Bundles (the real config source)
      // arrive in Phase B and replace this argument, not the line's shape.
      config: declaredBy,
    }),
  };
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
