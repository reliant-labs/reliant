// Copyright (c) 2025 Reliant Labs

/**
 * An org-scoped dashboard (`scope: "org"`) reads every project's telemetry in
 * the organization. The reader names no project; the environments are every
 * environment of the org, listed as `project/env` (obsPlace.orgEnvName), and
 * the reader may narrow them. Who may read is still the server's decision:
 * org membership, and every env id must be a live environment of that org.
 */

import { defaultParseSearch } from "@tanstack/react-router";

import type { ObsScope } from "@/services/controlPlane/observability/types";

import { ENV_ALL } from "../../Console/envScope";
import { consolePath, orgPath, withSearch } from "../../Console/paths";
import { exploreHrefFor, type ExploreHref } from "../Metrics/pivots";
import type { ObsEnvironment } from "../obsPlace";
import type { ObsSearch } from "../obsSearch";
import type { DashboardHrefs } from "./DashboardView";

export interface OrgSelection {
  /** The picked environments that exist, in the list's order. */
  names: string[];
  /** The picked environments themselves. */
  picked: ObsEnvironment[];
  /** Their ids. Empty: no narrowing, every environment of the org. */
  ids: string[];
  /** The one picked environment's name, else ENV_ALL: what single-environment features (compare, alerts) key on. */
  env: string;
}

/** Picked names that the org no longer has are dropped rather than scoping the page to nothing. */
export function orgSelection(orgEnvironments: readonly ObsEnvironment[], picked: readonly string[] | undefined): OrgSelection {
  const chosen = new Set(picked ?? []);
  const named = orgEnvironments.filter((candidate) => chosen.has(candidate.name));
  return { names: named.map((candidate) => candidate.name), picked: named, ids: named.map((candidate) => candidate.id), env: named.length === 1 ? named[0].name : ENV_ALL };
}

/** The query scope: the org and its picked environments, never a project. */
export function orgQueryScope(orgId: string, selection: OrgSelection): ObsScope {
  return { orgId, envIds: selection.ids, filters: [] };
}

/** What the scope reads, in words. */
export function orgScopeLabel(selection: OrgSelection): string {
  if (selection.names.length === 0) return "all projects · all environments";
  return selection.names.length <= 2 ? selection.names.join(", ") : `${selection.names.length} environments`;
}

/**
 * Where an org dashboard's links go. Its panels read other projects'
 * telemetry, so the explorers it opens are the org's (/forge/observability),
 * narrowed to the picked environments by their `project/env` names, never the
 * route project's console, which would show that project's data instead. The
 * dashboard's own pages (the list, versions, releases) stay the console's.
 */
export function orgDashboardHrefs(console: DashboardHrefs, selection: OrgSelection): DashboardHrefs {
  const envs = selection.names.length ? selection.names : undefined;
  const view = (name: "logs" | "traces" | "sessions") => (search: ObsSearch) => withSearch(orgPath({ kind: "obs-view", view: name }), { ...search, envs });
  return {
    ...console,
    logs: view("logs"),
    traces: view("traces"),
    trace: (traceId) => view("traces")({ trace: traceId, range: "7d" }),
    session: (sessionId) => view("sessions")({ session: sessionId }),
    workload: (name) => orgWorkloadHref(selection, name),
  };
}

/**
 * A workload is one per environment of one project: its page is that
 * project's, under the environment's bare name. With anything but one
 * environment picked there is no single workload, so there is no link ("").
 */
export function orgWorkloadHref(selection: OrgSelection, name: string): string {
  if (selection.picked.length !== 1) return "";
  const env = selection.picked[0];
  if (!env.project) return "";
  const bare = env.name.startsWith(`${env.project}/`) ? env.name.slice(env.project.length + 1) : env.name;
  return consolePath(env.project, { kind: "workload", env: bare, name });
}

/** "Explore in Metrics" for an org dashboard: the org's metrics explorer, on the picked environments. */
export function orgExploreHref(selection: OrgSelection): ExploreHref {
  const explore = exploreHrefFor(orgPath({ kind: "obs-view", view: "metrics" }), "");
  const envs = selection.names.length ? selection.names : undefined;
  return (pivot, options) => {
    const href = explore(pivot, { ...options, env: "" });
    if (!href || !envs) return href;
    const at = href.indexOf("?");
    const path = at < 0 ? href : href.slice(0, at);
    return withSearch(path, { ...(at < 0 ? {} : defaultParseSearch(href.slice(at))), envs });
  };
}
