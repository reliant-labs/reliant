// Copyright (c) 2025 Reliant Labs

/**
 * WHERE the Observability panel is, and which environments that means.
 *
 * Environment is a filter, not a place (owner decision 1), but the panel does
 * live in two places: the project's area, where the environment filter is the
 * reader's to set, and an env page's tab, where it is pinned.
 */

import type { CloudEnv } from "@/services/forge/cloudEnvs";
import type { LiveShape } from "@/services/forge/live";

import type { ObsSearch } from "./obsSearch";

/** What an environment's recorded shape says about alerting: its rules, routes and opt-outs. */
export type ObsDeclared = Pick<LiveShape, "alerts" | "alertRoutes" | "observability">;

export interface ObsEnvironment {
  /** control-plane environment id. */
  id: string;
  name: string;
  /** The forge app the env belongs to (org place only). */
  project?: string;
  /**
   * The env's recorded shape, for the Alerts area: which rules it declares and
   * where each came from. Null or absent: nothing recorded yet.
   */
  declared?: ObsDeclared | null;
}

/** Every environment the place covers, with what it declares. */
export function placeEnvironments(place: ObsPlace): ObsEnvironment[] {
  return place.kind === "env" ? [place.env] : place.environments;
}

/** "shop/prod"; a project-less env is its bare name. */
export function orgEnvName(project: string, env: string): string {
  return project ? `${project}/${env}` : env;
}

/** Display label for an app name: "" is a dashboard that belongs to no app. */
export function appLabel(project: string): string {
  return project || "Unassigned";
}

/**
 * The org's environments as an org place: each one named `app/env` (names
 * are only unique within an app), and the apps that own them, both sorted.
 * The control plane's org read (hooks/apps-queries.ts useOrgEnvironments)
 * already leaves out environments that name no app.
 */
export function orgPlaceOf(envs: readonly CloudEnv[]): { environments: ObsEnvironment[]; apps: string[] } {
  const environments = envs
    .map((env) => ({ id: env.id, name: orgEnvName(env.project, env.name), project: env.project, declared: null }))
    .sort((a, b) => a.name.localeCompare(b.name));
  const apps = [...new Set(envs.map((env) => env.project))].sort((a, b) => a.localeCompare(b));
  return { environments, apps };
}

export type ObsPlace =
  | {
      kind: "env";
      env: ObsEnvironment;
      /** The project area, carrying the current view: "Compare across environments". */
      onWiden?: (search: ObsSearch) => void;
    }
  | {
      kind: "project";
      /** The forge project (control-plane `deploy_environments.project`). */
      project: string;
      /** Every environment of the project the caller can see, for the picker. */
      environments: ObsEnvironment[];
    }
  | {
      kind: "org";
      /** Every environment of the org; `name` is `orgEnvName(project, env)`. */
      environments: ObsEnvironment[];
      /** Every app name, in display order. */
      apps: string[];
    };

/**
 * The scope's environment ids: the pinned one, or the picked names resolved
 * (empty = every environment). On an org place the picked names are narrowed
 * to the selected `apps`; with none picked, every environment of the selected
 * apps, and with no apps either, [] (the whole org). Apps that own no
 * environment also give [] — which the wire reads as the WHOLE org, so the
 * caller must check for that case first (`orgSelectionIsEmpty`).
 */
export function envIdsOf(place: ObsPlace, picked: readonly string[] | undefined, apps?: readonly string[]): string[] {
  if (place.kind === "env") return [place.env.id];
  if (place.kind === "org") {
    const inApps = place.environments.filter((env) => !apps?.length || apps.includes(env.project ?? ""));
    const chosen = new Set(picked ?? []);
    const named = inApps.filter((env) => chosen.has(env.name));
    if (named.length) return named.map((env) => env.id);
    return apps?.length ? inApps.map((env) => env.id) : [];
  }
  const byName = new Map(place.environments.map((env) => [env.name, env.id]));
  return (picked ?? []).flatMap((name) => {
    const id = byName.get(name);
    return id ? [id] : [];
  });
}

/** Apps are selected but own no environment: nothing to read (an empty id list would mean the whole org). */
export function orgSelectionIsEmpty(place: ObsPlace, picked: readonly string[] | undefined, apps: readonly string[] | undefined): boolean {
  return place.kind === "org" && !!apps?.length && envIdsOf(place, picked, apps).length === 0;
}

/** The environments an org place's picker offers: those of the selected apps (all, when none). */
export function orgPickerEnvironments(place: ObsPlace, apps: readonly string[] | undefined): ObsEnvironment[] {
  if (place.kind !== "org") return [];
  return apps?.length ? place.environments.filter((env) => apps.includes(env.project ?? "")) : place.environments;
}
