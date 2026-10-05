// Copyright (c) 2025 Reliant Labs

/**
 * THE ENVIRONMENT ROSTER — the one list the sidebar and the Overview read.
 *
 * ── TWO SOURCES, AND THEY ARE NOT PEERS ─────────────────────────────────────
 *
 *   backend   the control plane's environments (GetLiveView). The record of
 *             what exists, what it runs and where it came from. Readable with
 *             the daemon asleep, from any browser.
 *   checkout  environments the user's code DECLARES (`deploy/kcl/<env>/`)
 *             that the control plane has no record of yet. Only the daemon can
 *             see them, because only the daemon has the files.
 *
 * Backend rows lead and are complete on their own. A checkout entry is an
 * ADDITION, labelled "not registered", never a silent merge: the old sidebar
 * unioned the two invisibly, so it listed four environments while the Overview
 * beside it said "No environments have been built yet" — and an asleep laptop
 * emptied the sidebar of environments the backend had all along.
 *
 * ── LIFECYCLE ───────────────────────────────────────────────────────────────
 *
 * `local` environments run on a developer machine via `forge env up` and are
 * never built or released; their page leads with what is RUNNING. Everything
 * else is deployed. Read from the strongest source available:
 *
 *   1. the control plane's kind (`local`), which is immutable once recorded;
 *   2. forge's declared lifecycle — on every topology row, so known on first
 *      load — or its runtime lifecycle once an env status has been read;
 *   3. forge's destination — host or compose processes only — for an env
 *      neither of the above speaks to.
 *
 * `mixed` is NOT taken as local: control-plane's prod is mixed too.
 */

import type { LiveEnv } from "./live";
import { destinationOf, type ForgeTopologyEnv } from "./topology";

export type EnvLifecycle = "local" | "deployed";
export type RosterSource = "backend" | "checkout";

export interface RosterEnv {
  name: string;
  source: RosterSource;
  lifecycle: EnvLifecycle;
  /** The control plane's row, when it has one. */
  live: LiveEnv | null;
  /** forge's topology row for this checkout, when the daemon answered. */
  forge: ForgeTopologyEnv | null;
}

/** Hints that only some callers have — e.g. an env status already read. */
export interface LifecycleHints {
  /** Env names forge's runtime said are local. */
  localByForge?: ReadonlySet<string>;
}

export function lifecycleOf(
  live: Pick<LiveEnv, "kind"> | null | undefined,
  forge: Pick<ForgeTopologyEnv, "destination" | "lifecycle"> | null | undefined,
  forgeSaysLocal = false
): EnvLifecycle {
  if (live?.kind === "local") return "local";
  if (live && live.kind !== "unknown") return "deployed";
  if (forgeSaysLocal) return "local";
  // forge's DECLARED lifecycle, carried on every topology row — known on first
  // load, before any env status has been read. A non-empty value that is not
  // "local" (e.g. "ephemeral") is a declaration too: it must not fall through
  // to the destination guess.
  const declared = (forge?.lifecycle ?? "").trim().toLowerCase();
  if (declared === "local") return "local";
  if (declared !== "") return "deployed";
  const destination = destinationOf(forge);
  return destination === "host" || destination === "compose" ? "local" : "deployed";
}

/**
 * Backend environments first (sorted), then checkout-only ones (sorted). An
 * environment the checkout no longer declares but the backend holds stays —
 * the backend is the record.
 */
export function forgeEnvRoster(
  liveEnvs: readonly LiveEnv[],
  forgeEnvs: readonly ForgeTopologyEnv[],
  hints: LifecycleHints = {}
): RosterEnv[] {
  const forgeByName = new Map<string, ForgeTopologyEnv>();
  for (const env of forgeEnvs) {
    if (env.env && env.declared !== false) forgeByName.set(env.env, env);
  }

  const backend: RosterEnv[] = [...liveEnvs]
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((live) => {
      const forge = forgeByName.get(live.name) ?? null;
      return {
        name: live.name,
        source: "backend" as const,
        lifecycle: lifecycleOf(live, forge, hints.localByForge?.has(live.name)),
        live,
        forge,
      };
    });

  const known = new Set(backend.map((env) => env.name));
  const checkout: RosterEnv[] = [...forgeByName.values()]
    .filter((forge) => !known.has(forge.env))
    .sort((a, b) => a.env.localeCompare(b.env))
    .map((forge) => ({
      name: forge.env,
      source: "checkout" as const,
      lifecycle: lifecycleOf(null, forge, hints.localByForge?.has(forge.env)),
      live: null,
      forge,
    }));

  return [...backend, ...checkout];
}
