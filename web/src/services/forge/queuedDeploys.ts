// Copyright (c) 2025 Reliant Labs

/**
 * DEPLOYS THIS SESSION SAW QUEUED, followed until they go out.
 *
 * A queued deploy (LiveHold) finishes WITHOUT anyone in front of it: someone
 * sets up billing — often in another tab, often someone else — and the
 * control plane rolls the release out on its own. The person who saw it
 * waiting has usually moved on by then, so the one useful thing to tell them
 * is when it is LIVE (or that it did not make it). This is the bookkeeping for
 * that, and nothing else: no history, no persistence. A reload starts clean —
 * the environment's page still says where everything stands.
 *
 * Pure functions over Live readings, so the whole lifecycle is testable with
 * object literals. The store and the notification live in
 * store/queuedDeployStore.ts and components/Forge/QueuedDeployNotifier.tsx.
 *
 * TYPE-ONLY IMPORTS, on purpose: the store is read by a watch mounted at the
 * app root, and a runtime import of live.ts would pull the control-plane
 * client and its generated messages into the entry chunk of every page.
 */

import type { LiveEnv } from "./live";

/** live.ts's isQueued, restated so this module stays type-only (see above). */
function isQueued(env: Pick<LiveEnv, "holds">): boolean {
  return env.holds.length > 0;
}

export interface WatchedDeploy {
  envId: string;
  envName: string;
  /** The forge project the environment belongs to — the Live query to poll. */
  forgeProject: string;
  /** True while the last reading still had it queued; false once it was released and is rolling out. */
  queued: boolean;
}

export interface SettledDeploy {
  deploy: WatchedDeploy;
  /** live: the release is confirmed running. failed: its rollout failed. */
  outcome: "live" | "failed";
  /** The release that settled — the current one, which a newer deploy may have replaced. */
  release: string;
}

export type WatchedDeploys = Readonly<Record<string, WatchedDeploy>>;

/**
 * Fold one Live reading of `forgeProject` into the watch.
 *
 * Every queued environment in the reading is watched from now on. A watched
 * one that is no longer queued SETTLES when the platform confirms the release
 * running (`live`) or reports its rollout failed (`failed`) — each exactly
 * once, because a settled deploy leaves the watch. Anything in between is a
 * release on its way out, still watched. A watched environment missing from
 * the reading was deleted, and is dropped without a word.
 *
 * Only a reading the control plane actually ANSWERED may be folded in: an
 * unavailable one lists no environments, which would drop every watch.
 *
 * Returns the SAME object when nothing changed, so a store can skip the
 * update.
 */
export function observeQueuedDeploys(
  watched: WatchedDeploys,
  forgeProject: string,
  envs: LiveEnv[]
): { watched: WatchedDeploys; settled: SettledDeploy[] } {
  const next: Record<string, WatchedDeploy> = { ...watched };
  const settled: SettledDeploy[] = [];
  let changed = false;

  const byId = new Map(envs.map((env) => [env.id, env]));
  for (const env of envs) {
    if (!isQueued(env)) continue;
    const current = watched[env.id];
    if (current?.queued && current.envName === env.name && current.forgeProject === forgeProject) continue;
    next[env.id] = { envId: env.id, envName: env.name, forgeProject, queued: true };
    changed = true;
  }

  for (const deploy of Object.values(watched)) {
    if (deploy.forgeProject !== forgeProject) continue;
    const env = byId.get(deploy.envId);
    if (!env) {
      delete next[deploy.envId];
      changed = true;
      continue;
    }
    if (isQueued(env)) continue;
    if (env.observed.state === "converged" || env.observed.state === "failed") {
      settled.push({ deploy, outcome: env.observed.state === "converged" ? "live" : "failed", release: env.release });
      delete next[deploy.envId];
      changed = true;
      continue;
    }
    if (deploy.queued) {
      next[deploy.envId] = { ...deploy, queued: false };
      changed = true;
    }
  }

  return { watched: changed ? next : watched, settled };
}

/**
 * How often to re-read `forgeProject` for the watch, or false when nothing
 * in it is watched.
 *
 * A release on its way out is read often — it settles in minutes, and that
 * moment is the notification. A deploy still queued is read rarely: it moves
 * only when a person acts, which may be days, and a fast poll would spend a
 * request every few seconds on a screen nobody is looking at.
 */
export const RELEASING_POLL_MS = 10_000;
export const QUEUED_POLL_MS = 60_000;

export function watchPollInterval(watched: WatchedDeploys, forgeProject: string): number | false {
  let any = false;
  for (const deploy of Object.values(watched)) {
    if (deploy.forgeProject !== forgeProject) continue;
    if (!deploy.queued) return RELEASING_POLL_MS;
    any = true;
  }
  return any ? QUEUED_POLL_MS : false;
}

/** The forge projects with anything watched, in a stable order. */
export function watchedProjects(watched: WatchedDeploys): string[] {
  return [...new Set(Object.values(watched).map((deploy) => deploy.forgeProject))].sort();
}
