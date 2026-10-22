// Copyright (c) 2025 Reliant Labs

/**
 * The CLUSTER INVENTORY — `forge env status <env> --json`'s top-level
 * `workloads` key, and the projection the Environments screen renders.
 *
 * WHAT THIS EXISTS TO FIX. The Environments screen used to render the report's
 * `services` array under a heading that read like a deployment inventory.
 * `services` is the list of HOST PROCESSES on the reader's own laptop. For
 * control-plane's prod that is two local dev servers, while the cluster runs
 * sixteen workloads — so the screen answered "what is running in prod?" with a
 * fact about the developer's machine, and a user reasonably asked why prod had
 * only two services. Forge now emits the real thing beside it; this module is
 * its type and its projection. `services` keeps its own, correctly-labelled
 * place on the Status screen.
 *
 * THE DOCUMENT IS NOT A BARE ARRAY, AND THAT IS THE POINT. "Forge looked and
 * found nothing" and "forge could not reach the cluster" are different facts,
 * and an empty array states the first while frequently meaning the second.
 * Forge therefore carries the distinction in `status` and lists a workload it
 * could not see ANYWAY, with status `unknown` and no pods. So:
 *
 *   BRANCH ON `status` FIRST, NEVER ON `workloads.length`.
 *
 * [posture] is that branch, made explicit so no component can skip it. An
 * unreachable prod renders "16 workloads, state unknown" — not an empty screen,
 * and never a green tick.
 *
 * THREE OMISSIONS THAT ARE NOT ZEROES:
 *
 *   - `desired_replicas` is ABSENT for kinds with no replica count (Job,
 *     CronJob). Absent and 0 are different claims; [replicas] keeps them apart
 *     so a completed Job never renders as "0/0", which reads as an outage.
 *   - `cluster` is EMPTY for a workload the render could not route. Forge
 *     deliberately refuses to fall back to kubectl's ambient current-context,
 *     and neither do we: an unrouted workload says so.
 *   - the whole `workloads` key is ABSENT from a forge too old to emit it.
 *     That is a third answer again — "nobody was asked" — and it must not
 *     render as "this environment deploys nothing".
 *
 * Statuses reuse the CHECK vocabulary from ./status (pass/fail/warn/unknown/
 * skip) rather than a parallel one invented here, because that is the
 * vocabulary forge itself put on these records.
 */

import type { ForgeCheckStatus } from "./status";

// ── The document, as forge emits it ─────────────────────────────────────────
//
// Typed against forge's internal/doctor/clusterinventory.go. Everything forge
// marks `omitempty` is optional here, and nothing is assumed non-null — a
// report from an older or newer forge must render, not throw.

/** One pod, reduced to what a status view shows. */
export interface ForgePodState {
  name?: string;
  /** The pod's Ready CONDITION — what decides whether traffic reaches it. */
  ready?: boolean;
  /** The "1/1" pair. */
  containers_ready?: number;
  containers?: number;
  /** Pod phase: "Running", "Pending", "Succeeded", … */
  phase?: string;
  restarts?: number;
}

/** One rendered pod-owning object, and what the cluster said about it. */
export interface ForgeWorkloadState {
  name?: string;
  /** Lowercased: "deployment", "job", "statefulset", … */
  kind?: string;
  /** EMPTY means the render did not say where this deploys. Never guessed. */
  cluster?: string;
  namespace?: string;
  status?: string;
  /** From the RENDER. ABSENT for kinds with no replica count — not zero. */
  desired_replicas?: number;
  /** Always 0 when status is unknown. Read it WITH status, never alone. */
  ready_replicas?: number;
  /** The MAX across pods, not the sum: one container dying 130 times is the signal. */
  restarts?: number;
  /** Job/CronJob: pods are expected to come and go, so 0 pods is not an alarm. */
  ephemeral?: boolean;
  pods?: ForgePodState[];
  /** Forge's own diagnoses, already assembled into actionable sentences. */
  findings?: string[];
}

/** One (kubectl context, namespace) pair forge probed. */
export interface ForgeClusterScope {
  /** The kubectl context name — always explicit, never the ambient current-context. */
  cluster?: string;
  namespace?: string;
  /** pass when the pod list was obtained, unknown when it was not. */
  status?: string;
  /** Why the probe failed, verbatim. */
  error?: string;
  /** How many of this env's workloads land here. Non-zero alongside an unknown
   *  status is the "we could not see N things" fact an empty array cannot express. */
  rendered_workloads?: number;
}

/** The structured answer to "what is deployed, and is it running?" for one env. */
export interface ForgeClusterInventory {
  status?: string;
  env?: string;
  clusters?: ForgeClusterScope[];
  workloads?: ForgeWorkloadState[];
}

// ── Posture: the branch every consumer must make ────────────────────────────

/**
 * What the inventory as a whole is telling you. FIVE answers, because every
 * merge available here erases a distinction forge went out of its way to keep.
 *
 *   not-reported  no `workloads` key at all — this forge predates it, or the
 *                 check was excluded. Nobody was asked. NOT "deploys nothing".
 *   undetermined  status unknown. The array is NOT an inventory however full
 *                 it looks; every row in it is a thing forge could not see.
 *   nothing       status pass and genuinely no workloads. This env really does
 *                 deploy nothing to Kubernetes. The only honest empty state.
 *   measured      status pass/fail/warn with rows. Real, current facts.
 *   inconsistent  status pass/fail/warn but no rows to back it — forge said it
 *                 measured and listed nothing. Reported rather than smoothed
 *                 over, because silently showing "deploys nothing" is how a
 *                 reporting bug becomes an outage nobody sees.
 */
export type InventoryPosture =
  | "not-reported"
  | "undetermined"
  | "nothing"
  | "measured"
  | "inconsistent";

export function posture(
  inventory: ForgeClusterInventory | null | undefined
): InventoryPosture {
  if (!inventory || typeof inventory !== "object") return "not-reported";
  // An unrecognised status from a newer forge lands here too, and that
  // direction is load-bearing: guessing "measured" for a status this build
  // cannot read is indistinguishable from a verified reading.
  if (inventory.status !== "pass" && inventory.status !== "fail" && inventory.status !== "warn") {
    return "undetermined";
  }
  const count = workloadsOf(inventory).length;
  if (count > 0) return "measured";
  return scopesOf(inventory).length === 0 ? "nothing" : "inconsistent";
}

/** True when the rows describe the cluster's ACTUAL state rather than a list of unknowns. */
export function isMeasured(inventory: ForgeClusterInventory | null | undefined): boolean {
  const p = posture(inventory);
  return p === "measured" || p === "nothing";
}

// ── Projections (pure) ──────────────────────────────────────────────────────

/** The inventory carried by an env-status report, or null when forge sent none. */
export function inventoryOf(
  report: { workloads?: ForgeClusterInventory } | null | undefined
): ForgeClusterInventory | null {
  const inventory = report?.workloads;
  if (!inventory || typeof inventory !== "object" || Array.isArray(inventory)) return null;
  return inventory;
}

/**
 * The workload rows, in forge's order, with NOTHING filtered out and nothing
 * dropped for being unnamed — a workload forge emitted and we could not label
 * is itself information, so it gets a positional name instead of vanishing.
 */
export function workloadsOf(
  inventory: ForgeClusterInventory | null | undefined
): Array<ForgeWorkloadState & { name: string }> {
  if (!inventory || !Array.isArray(inventory.workloads)) return [];
  return inventory.workloads
    .filter((w): w is ForgeWorkloadState => !!w && typeof w === "object")
    .map((w, index) => ({ ...w, name: w.name || `Workload ${index + 1}` }));
}

/** The (context, namespace) scopes forge read, unfiltered. */
export function scopesOf(
  inventory: ForgeClusterInventory | null | undefined
): ForgeClusterScope[] {
  if (!inventory || !Array.isArray(inventory.clusters)) return [];
  return inventory.clusters.filter((s): s is ForgeClusterScope => !!s && typeof s === "object");
}

/** The pods matched to a workload, unfiltered and positionally named. */
export function podsOf(
  workload: ForgeWorkloadState | null | undefined
): Array<ForgePodState & { name: string }> {
  if (!workload || !Array.isArray(workload.pods)) return [];
  return workload.pods
    .filter((p): p is ForgePodState => !!p && typeof p === "object")
    .map((p, index) => ({ ...p, name: p.name || `Pod ${index + 1}` }));
}

/**
 * Where a workload was read from, as one displayable answer.
 *
 * `routed: false` is the empty-cluster case. Forge never falls back to
 * kubectl's current context for it, so neither does this: the caller is handed
 * the absence, not a plausible-looking guess.
 */
export interface WorkloadScope {
  routed: boolean;
  cluster: string;
  namespace: string;
}

export function scopeOf(workload: ForgeWorkloadState): WorkloadScope {
  const cluster = workload.cluster ?? "";
  return { routed: cluster !== "", cluster, namespace: workload.namespace ?? "" };
}

/** A scope's identity, for counting how many distinct places rows came from. */
export function scopeKey(scope: { cluster?: string; namespace?: string }): string {
  return `${scope.cluster ?? ""}/${scope.namespace ?? ""}`;
}

/**
 * How many distinct (cluster, namespace) pairs the ROWS came from, including
 * the unrouted pseudo-scope. One means the table needs no per-row location
 * column, because a single scope named once above it is unambiguous. Two —
 * control-plane's dev spans k3d-control-plane and k3d-cp-daemon — means every
 * row has to say where it is, or the reader cannot tell.
 */
export function distinctScopes(
  inventory: ForgeClusterInventory | null | undefined
): number {
  const keys = new Set<string>();
  for (const workload of workloadsOf(inventory)) keys.add(scopeKey(workload));
  return keys.size;
}

/**
 * The replica reading, three-valued — this is where "omitted is not zero"
 * becomes pixels.
 *
 *   counted       desired is a real number. "2/2", and `short` is true when
 *                 fewer are ready than asked for.
 *   no-replicas   desired was OMITTED. The kind has no replica count at all
 *                 (Job, CronJob), so there is no ratio to state and "0/0"
 *                 would invent a failing one.
 *   unmeasured    status is unknown, so ready_replicas is 0 because nothing
 *                 was read — not because nothing is running. Rendering the
 *                 zero here is precisely how an unreachable cluster comes to
 *                 look like a total outage.
 */
export type ReplicaReading =
  | { kind: "counted"; ready: number; desired: number; short: boolean }
  | { kind: "no-replicas"; ephemeral: boolean }
  | { kind: "unmeasured"; desired: number | null };

export function replicas(workload: ForgeWorkloadState): ReplicaReading {
  const desired = typeof workload.desired_replicas === "number" ? workload.desired_replicas : null;
  if (workload.status === "unknown") return { kind: "unmeasured", desired };
  if (desired === null) return { kind: "no-replicas", ephemeral: workload.ephemeral === true };
  const ready = typeof workload.ready_replicas === "number" ? workload.ready_replicas : 0;
  return { kind: "counted", ready, desired, short: ready < desired };
}

/** The findings forge already phrased for a human. Never parsed, only shown. */
export function findingsOf(workload: ForgeWorkloadState | null | undefined): string[] {
  if (!workload || !Array.isArray(workload.findings)) return [];
  return workload.findings.filter((f): f is string => typeof f === "string" && f !== "");
}

/**
 * Counts per status, summed from the rows that will be RENDERED rather than
 * read off a header field, so the stat strip can never claim a tally the table
 * beneath it contradicts.
 */
export function statusTally(
  inventory: ForgeClusterInventory | null | undefined
): Record<ForgeCheckStatus, number> {
  const totals: Record<ForgeCheckStatus, number> = {
    pass: 0,
    fail: 0,
    warn: 0,
    skip: 0,
    unknown: 0,
  };
  for (const workload of workloadsOf(inventory)) {
    const status = workload.status;
    if (status === "pass" || status === "fail" || status === "warn" || status === "skip") {
      totals[status] += 1;
    } else {
      // Absent or unrecognised falls to unknown, never to pass.
      totals.unknown += 1;
    }
  }
  return totals;
}

/**
 * The one-line sentence the screen leads with. It is generated from the
 * posture and the scopes together because the two facts are only useful as a
 * pair: "16 workloads" means nothing until the reader knows where they were
 * counted, and that ambiguity is exactly what made the old screen misleading.
 */
export function inventorySentence(
  inventory: ForgeClusterInventory | null | undefined,
  env: string
): string {
  const count = workloadsOf(inventory).length;
  const where = scopeSentence(inventory);
  switch (posture(inventory)) {
    case "not-reported":
      return `This forge did not report cluster workloads for ${env}. Nothing here has been measured — it is not a statement that ${env} deploys nothing.`;
    case "undetermined":
      return count > 0
        ? `${env} declares ${count} ${count === 1 ? "workload" : "workloads"}${where}, and forge could not read their state. This is neither healthy nor broken — it is unknown.`
        : `Forge could not determine what ${env} deploys${where}.`;
    case "nothing":
      return `${env} deploys nothing to Kubernetes. Forge looked and there is genuinely nothing to run.`;
    case "inconsistent":
      return `Forge reported a reading for ${env}${where} but listed no workloads. That combination should not occur, so treat this as a hole in the report rather than as an empty environment.`;
    case "measured":
      return `${count} ${count === 1 ? "workload" : "workloads"} rendered for ${env}${where}.`;
  }
}

/** " on <context>/<namespace>" — or a plural form, or nothing to say. */
function scopeSentence(inventory: ForgeClusterInventory | null | undefined): string {
  const scopes = scopesOf(inventory).filter((s) => (s.cluster ?? "") !== "");
  if (scopes.length === 0) return "";
  if (scopes.length === 1) {
    const only = scopes[0];
    return ` on ${only.cluster}${only.namespace ? `/${only.namespace}` : ""}`;
  }
  return ` across ${scopes.length} clusters`;
}
