// Copyright (c) 2025 Reliant Labs

/**
 * THE ENVIRONMENT SCOPE: environment is a filter in the top
 * bar, on every page, not a place.
 *
 *   - It is `?env=<name>` or `?env=all`, the one scope param every list page
 *     honours. A page whose path names an environment pins that one instead.
 *   - It is remembered PER PROJECT (localStorage), so coming back to a
 *     project opens the scope you left it on.
 *   - The default is `prod`, not All: the question a reader opens the console
 *     with is almost always about production, and a page summed over every
 *     preview and dev stack answers it worse.
 *
 * A remembered or linked environment that no longer exists (a preview torn
 * down, a typo) falls back to prod, then All, rather than scoping every page
 * to nothing.
 */

import type { RosterEnv } from "@/services/forge/roster";

export const ENV_ALL = "all";
/** What the scope control says on a dashboard whose signals belong to no environment. */
export const PLATFORM_SCOPE_LABEL = "platform-wide (not per environment)";
export const DEFAULT_ENV = "prod";

const storageKey = (project: string) => `forge.console.env.${project}`;

export function rememberedEnv(project: string): string | null {
  try {
    return window.localStorage.getItem(storageKey(project));
  } catch {
    return null;
  }
}

export function rememberEnv(project: string, env: string): void {
  try {
    window.localStorage.setItem(storageKey(project), env);
  } catch {
    // Storage full or disabled: the scope still works for this visit.
  }
}

/**
 * The scope in effect. `known` is the project's environment names, or null
 * while they are still arriving — until then the candidate stands, so the
 * first render already reads the right scope instead of All-then-prod.
 */
export function resolveEnvScope(
  param: string | undefined,
  remembered: string | null,
  known: readonly string[] | null
): string {
  const candidate = param || remembered || DEFAULT_ENV;
  if (known === null || candidate === ENV_ALL || known.includes(candidate)) return candidate;
  return known.includes(DEFAULT_ENV) ? DEFAULT_ENV : ENV_ALL;
}

/** The environment names a scope covers: one, or every one of `envs`. */
export function envsInScope<T extends { name: string }>(env: string, envs: readonly T[]): T[] {
  return env === ENV_ALL ? [...envs] : envs.filter((candidate) => candidate.name === env);
}

export interface ScopeOption {
  name: string;
  /** Recorded by Reliant (false: declared only in the checkout). */
  recorded: boolean;
}

/**
 * The scope control's choices: the long-lived environments in the segment —
 * prod first, then what Reliant runs, then local, then declared-only — and
 * previews collapsed into one menu, because there can be dozens and each
 * lives for one pull request.
 */
export function scopeOptions(envs: readonly RosterEnv[]): { long: ScopeOption[]; previews: ScopeOption[] } {
  const rank = (env: RosterEnv) =>
    env.name === DEFAULT_ENV ? 0 : env.source === "checkout" ? 3 : env.lifecycle === "local" ? 2 : 1;
  const long = envs
    .filter((env) => env.live?.kind !== "preview")
    .sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name))
    .map((env) => ({ name: env.name, recorded: env.source === "backend" }));
  const previews = envs
    .filter((env) => env.live?.kind === "preview")
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((env) => ({ name: env.name, recorded: true }));
  return { long, previews };
}
