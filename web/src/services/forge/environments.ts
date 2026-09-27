// Copyright (c) 2025 Reliant Labs

/**
 * ONE LIST OF ENVIRONMENTS, FROM TWO SOURCES THAT FAIL INDEPENDENTLY.
 *
 * The forge surface is organised around environments, and an environment is
 * known to at most two parties:
 *
 *   forge (daemon)   topology.ts — every env the checkout DECLARES, with its
 *                    destination, release binding and image digests. Needs the
 *                    user's daemon, because forge runs against their files.
 *   control plane    cloudEnvs.ts — every env the control plane HOLDS for this
 *                    forge project: the ones it runs (Reliant cloud) and the
 *                    LOCAL ones it keeps secrets for. Needs no daemon.
 *
 * The list is their UNION, joined by environment name within one forge
 * project. Neither side waits for the other: a daemon that is asleep leaves
 * the control plane's environments on screen, and a control plane that is
 * unreachable leaves forge's. That is the whole design constraint — a missing
 * daemon must never blank an environment the control plane can describe.
 *
 * No React, no styling. Pure functions over the two reports, so the join and
 * every derived fact is testable with object literals.
 */

import { CONTROL_PLANE_API_URL } from "@/services/controlPlane/config";

import type { CloudEnv, CloudEnvStatus, CloudPromotion } from "./cloudEnvs";
import { managedStoreTarget, normalizeEndpoint, type ManagedStoreTarget } from "./secretStore";
import type { ForgeEnvStatusReport } from "./status";
import {
  certaintyOf,
  describeLag,
  destinationOf,
  envBindingState,
  envHostedVerdict,
  hostedWorkloadsOf,
  isDirtyRelease,
  type Certainty,
  type EnvBindingState,
  type ForgeHostedWorkload,
  type ForgeOutcome,
  type ForgeTopologyEnv,
  type ForgeTopologyReport,
  type HostedVerdict,
} from "./topology";

// ── Where an environment runs ───────────────────────────────────────────────

/**
 * The answer to "where does this run?", in the words a reader uses.
 *
 * Coarser than forge's destination on purpose — compose and host are both
 * "this machine" to the person reading the screen — but never coarser in a
 * way that invents a fact: `mixed` stays mixed, and `unknown` never becomes
 * `cluster`.
 *
 *   local    a developer machine: `forge env up` runs it. A LOCAL control
 *            plane env, or forge destination host / compose.
 *   cloud    Reliant cloud: the control plane runs it (a PERSISTENT or
 *            PREVIEW control plane env, or forge destination hosted).
 *   cluster  a Kubernetes cluster forge deploys to by kube context.
 *   mixed    workloads go to more than one kind of destination.
 *   static   a static site on a CDN / object store forge publishes to.
 *   external a target forge hands off to.
 *   unknown  nobody said, or said something this build does not recognise.
 */
export type EnvWhere = "local" | "cloud" | "cluster" | "mixed" | "static" | "external" | "unknown";

/**
 * The control plane's kind wins over forge's destination, because it is the
 * durable fact: an env's kind is immutable on the control plane, whereas a
 * destination is re-derived from whatever KCL the daemon's checkout holds.
 */
export function whereOf(
  forge: Pick<ForgeTopologyEnv, "destination"> | null | undefined,
  cloud: Pick<CloudEnv, "kind"> | null | undefined
): EnvWhere {
  if (cloud?.kind === "local") return "local";
  if (cloud?.kind === "persistent" || cloud?.kind === "preview") return "cloud";
  switch (destinationOf(forge)) {
    case "hosted":
      return "cloud";
    case "host":
    case "compose":
      return "local";
    case "cluster":
      return "cluster";
    case "mixed":
      return "mixed";
    case "static":
      return "static";
    case "external":
      return "external";
    default:
      return "unknown";
  }
}

export function whereLabel(where: EnvWhere): string {
  switch (where) {
    case "local":
      return "Local";
    case "cloud":
      return "Reliant cloud";
    case "cluster":
      return "Cluster";
    case "mixed":
      return "Mixed";
    case "static":
      return "Static hosting";
    case "external":
      return "External";
    default:
      return "Unknown";
  }
}

export function whereExplanation(where: EnvWhere): string {
  switch (where) {
    case "local":
      return "Runs on a developer machine: `forge env up` starts it. Nothing is deployed anywhere.";
    case "cloud":
      return "Runs on Reliant cloud. The control plane deploys it and reports its state — no daemon is needed to see it.";
    case "cluster":
      return "Deployed to a Kubernetes cluster this environment's KCL names by kube context.";
    case "mixed":
      return "Workloads in this environment go to more than one kind of destination — for example a cluster and a static host.";
    case "static":
      return "A static site forge publishes to object storage or a CDN.";
    case "external":
      return "Deployed by an external target forge hands off to.";
    default:
      return "Neither forge nor the control plane said where this environment runs. It is not assumed to be a cluster.";
  }
}

// ── The join ────────────────────────────────────────────────────────────────

export interface ForgeEnvSummary {
  name: string;
  where: EnvWhere;
  /** forge's row. Null when the daemon did not answer, or this checkout does not declare the env. */
  forge: ForgeTopologyEnv | null;
  /** The control plane's row. Null when it holds no such env for this project, or could not be asked. */
  cloud: CloudEnv | null;
}

/**
 * joinEnvironments unions the two lists by name.
 *
 * forge's order first (it is the order the project declares), then any env
 * only the control plane knows — which is exactly the set a sleeping daemon
 * would otherwise make disappear — by name.
 */
export function joinEnvironments(
  forgeEnvs: ForgeTopologyEnv[],
  cloudEnvs: CloudEnv[]
): ForgeEnvSummary[] {
  const cloudByName = new Map<string, CloudEnv>();
  for (const env of cloudEnvs) cloudByName.set(env.name, env);

  const out: ForgeEnvSummary[] = [];
  const seen = new Set<string>();
  for (const forge of forgeEnvs) {
    if (seen.has(forge.env)) continue;
    seen.add(forge.env);
    const cloud = cloudByName.get(forge.env) ?? null;
    out.push({ name: forge.env, where: whereOf(forge, cloud), forge, cloud });
  }
  for (const cloud of [...cloudEnvs].sort((a, b) => a.name.localeCompare(b.name))) {
    if (seen.has(cloud.name)) continue;
    seen.add(cloud.name);
    out.push({ name: cloud.name, where: whereOf(null, cloud), forge: null, cloud });
  }
  return out;
}

/**
 * True when the control plane RUNS this env — so its workloads, health and
 * promotion ledger come from DeployService, with no daemon involved.
 * A LOCAL control plane env is NOT one: the platform never deploys it.
 */
export function isCloudRun(summary: Pick<ForgeEnvSummary, "cloud">): boolean {
  return summary.cloud?.kind === "persistent" || summary.cloud?.kind === "preview";
}

/**
 * The control-plane id to read a cloud-run env's status and ledger by.
 *
 * The control plane's own row first. Failing that, a HOSTED env forge has
 * ensured names its id in forge's report — and that id is used too, under the
 * same guard the managed store applies (managedStoreTarget: hosted, ensured,
 * and on THIS console's control plane). That covers an env the project-scoped
 * list did not return: one ensured before environments had a project, or a
 * control plane that predates the field. Never a guessed id.
 */
export function cloudRunIdOf(summary: Pick<ForgeEnvSummary, "cloud" | "forge">): string | null {
  if (isCloudRun(summary)) return summary.cloud!.id;
  if (summary.cloud) return null; // LOCAL (or unknown kind): the platform runs nothing.
  if (destinationOf(summary.forge) !== "hosted") return null;
  const target = managedStoreTarget(summary.forge);
  return target.kind === "lookup" ? target.environmentId : null;
}

/** True for a LOCAL control plane env: its secrets live in the managed store; it runs via `forge env up`. */
export function isCloudLocal(summary: Pick<ForgeEnvSummary, "cloud">): boolean {
  return summary.cloud?.kind === "local";
}

/**
 * Whether `forge env up` is running this environment's stack on THIS machine
 * right now — any host service it launched and owns.
 *
 * This, not the destination, is what decides whether an environment has a
 * Dev stack section beyond a pure local env. control-plane's `dev` is
 * `mixed` (host processes AND k3d clusters AND compose) and is the stack a
 * developer runs every day; its prod declares host frontends too, but nothing
 * forge owns is running them. `owned` is forge's own measurement of the
 * difference, so a prod whose `services` array merely lists what `env up`
 * WOULD launch never gets a dev-stack panel — which is how it once appeared
 * to run two services while its cluster ran sixteen.
 */
export function devStackRunsHere(report: Pick<ForgeEnvStatusReport, "services"> | null | undefined): boolean {
  return (report?.services ?? []).some((service) => service?.owned === true);
}

/**
 * Whether promote/deploy are offered at all. Never for a LOCAL env — the
 * control plane refuses both, and there is nothing to deploy: `forge env up`
 * runs the working tree. Everything else goes through forge on the daemon.
 */
export function offersShipping(summary: Pick<ForgeEnvSummary, "where" | "cloud">): boolean {
  return summary.where !== "local" && !isCloudLocal(summary);
}

// ── Row facts ───────────────────────────────────────────────────────────────

/**
 * What an environment's health cell says, by source. Three shapes because the
 * three sources measure three different things, and a single verdict word
 * across all of them would claim they were comparable:
 *
 *   hosted   the control plane's verdict over its deployments.
 *   images   forge's per-image certainty for a cluster binding — `not_verified`
 *            until someone runs verify, and that is rendered as such.
 *   none     nothing measures this env from here (a local env, an unbound
 *            one). Not healthy and not broken — just not a thing we report.
 */
export type EnvHealth =
  | { kind: "hosted"; verdict: HostedVerdict; loading?: boolean }
  | { kind: "images"; tally: Record<Certainty, number> }
  | { kind: "not-deployed" }
  | { kind: "none" };

export interface EnvFacts {
  release: string | null;
  /** RFC3339 PROMOTE time. Promotion writes a pointer; deployment moves bytes. */
  promotedAt?: string;
  /** The current ledger entry was a rollback — the fact on this screen most worth seeing. */
  rolledBack: boolean;
  dirty: boolean;
  lag: string | null;
  binding: EnvBindingState | "cloud" | "local";
  health: EnvHealth;
  /** A hosted env's workloads: from the control plane when it answered, else from forge's report. */
  workloads: ForgeHostedWorkload[];
}

/**
 * envFacts derives one row's facts from whichever sources answered.
 *
 * For an env the control plane runs, the control plane is authoritative and
 * forge is NOT consulted for release or health: forge read those same facts
 * from the same control plane, but only when a daemon was awake to ask. For
 * everything else forge's topology is the only source there is.
 */
export function envFacts(
  summary: ForgeEnvSummary,
  cloudStatus: CloudEnvStatus | undefined,
  cloudStatusLoading = false
): EnvFacts {
  if (isCloudLocal(summary)) {
    return {
      release: null,
      rolledBack: false,
      dirty: false,
      lag: null,
      binding: "local",
      health: { kind: "none" },
      workloads: [],
    };
  }

  if (cloudRunIdOf(summary)) {
    const promotion: CloudPromotion | null = cloudStatus?.currentPromotion ?? null;
    const workloads = cloudStatus?.workloads ?? [];
    return {
      release: promotion?.releaseVersion || null,
      promotedAt: promotion?.createdAt,
      rolledBack: promotion?.kind === "rollback",
      // The control plane's ledger is authoritative for the binding, but
      // provenance (dirty tree) and lag are forge's release-ledger facts.
      // Carried only when forge is describing the SAME release.
      dirty:
        !!summary.forge && summary.forge.release === promotion?.releaseVersion && isDirtyRelease(summary.forge),
      lag:
        summary.forge && summary.forge.release === promotion?.releaseVersion
          ? describeLag(summary.forge.lag)
          : null,
      binding: "cloud",
      health:
        cloudStatus || cloudStatusLoading
          ? { kind: "hosted", verdict: cloudStatus?.verdict ?? "unknown", loading: !cloudStatus }
          : { kind: "hosted", verdict: "unknown" },
      workloads,
    };
  }

  const forge = summary.forge;
  if (!forge) {
    return {
      release: null,
      rolledBack: false,
      dirty: false,
      lag: null,
      binding: "unbound",
      health: { kind: "none" },
      workloads: [],
    };
  }

  const hosted = summary.where === "cloud";
  const images = forge.images ?? [];
  const tally: Record<Certainty, number> = { "known-good": 0, "known-bad": 0, unknown: 0 };
  for (const img of images) tally[certaintyOf(img.state ?? "not_verified")] += 1;

  return {
    release: forge.release || null,
    promotedAt: forge.promoted_at,
    rolledBack: forge.kind === "rollback",
    dirty: isDirtyRelease(forge),
    lag: describeLag(forge.lag),
    binding: envBindingState(forge),
    health: hosted
      ? (forge.environment_id ?? "").trim() === ""
        ? { kind: "not-deployed" }
        : { kind: "hosted", verdict: envHostedVerdict(forge) }
      : images.length > 0
        ? { kind: "images", tally }
        : { kind: "none" },
    workloads: hosted ? hostedWorkloadsOf(forge) : [],
  };
}

// ── The daemon's side, as one state ─────────────────────────────────────────

/**
 * What forge's side of the list is doing. `offline` is the daemon not
 * answering at all (a transport failure); the other non-report states are
 * forge's own answers, each rendered as itself by ForgeStates.
 */
export type DaemonSide =
  | "loading"
  | "ok"
  | "offline"
  | "not-forge-project"
  | "unsupported"
  | "malformed"
  | "unreachable";

export function daemonSideOf(
  outcome: ForgeOutcome<ForgeTopologyReport> | undefined,
  error: unknown
): DaemonSide {
  if (outcome) {
    return outcome.kind === "report" ? "ok" : outcome.kind;
  }
  // No answer yet and no error is "still asking" — including the tick before
  // a project id resolves, when the query has not been enabled at all.
  return error ? "offline" : "loading";
}

// ── The managed store, keyed on whichever source can name the env ──────────

/**
 * Which managed-store id this environment's secrets live under — or why none.
 *
 * The control plane's own row is the best source: its id was issued by THIS
 * console's control plane, so it needs no endpoint check, and it exists for a
 * LOCAL env too (whose secrets `forge env up` pulls). Only when the control
 * plane has no row does forge's report get a say, through the existing
 * managedStoreTarget — which refuses a non-hosted env, a never-ensured one,
 * and one on a different control plane rather than guessing an id.
 */
export function managedTargetFor(summary: Pick<ForgeEnvSummary, "forge" | "cloud">): ManagedStoreTarget {
  if (!CONTROL_PLANE_API_URL) return { kind: "none", availability: "no-control-plane" };
  if (summary.cloud) {
    return { kind: "lookup", environmentId: summary.cloud.id, endpoint: normalizeEndpoint(CONTROL_PLANE_API_URL) };
  }
  return managedStoreTarget(summary.forge);
}

// ── The join key: the forge project's name ──────────────────────────────────

/**
 * The control plane files a forge project's environments under forge's NAME
 * for it — the `name` in forge.yaml, which forge sends as the environment's
 * `project` when it ensures one. So that name, not the Reliant project's
 * display name, is the join key.
 *
 * Reliant persists it on the project row (Project.forge_project_name): read
 * from forge.yaml when the project is created, and refreshed by the server
 * every time the daemon's topology report names the project. That is what
 * keeps a project's cloud environments on screen while the laptop that runs
 * forge is asleep, in any browser.
 *
 * The live report wins when there is one — it is forge's answer this second,
 * and the row catches up on the same RPC. A project whose row has no name
 * (never discovered by a daemon) has nothing to look up, and the screen says
 * so rather than guessing one from the Reliant project's display name — a
 * guess that matched another project's `prod` would put the wrong environment
 * beside a Promote button.
 */
export interface ForgeProjectName {
  name: string | null;
  /** daemon: forge just said so. project: the name Reliant persisted on the project row. */
  source: "daemon" | "project" | null;
}

export function resolveForgeProjectName(
  persisted: string | null | undefined,
  outcome: ForgeOutcome<ForgeTopologyReport> | undefined
): ForgeProjectName {
  const reported = outcome?.kind === "report" ? (outcome.report.project ?? "").trim() : "";
  if (reported !== "") return { name: reported, source: "daemon" };
  const stored = (persisted ?? "").trim();
  return stored !== "" ? { name: stored, source: "project" } : { name: null, source: null };
}
