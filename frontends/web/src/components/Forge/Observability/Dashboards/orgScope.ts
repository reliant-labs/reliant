// Copyright (c) 2025 Reliant Labs

/**
 * An org-scoped dashboard (`scope: "org"`) reads every project's telemetry in
 * the organization. The reader names no project; the environments are every
 * environment of the org, listed as `project/env` (obsPlace.orgEnvName), and
 * the reader may narrow them. Who may read is still the server's decision:
 * org membership, and every env id must be a live environment of that org.
 */

import type { ObsScope } from "@/services/controlPlane/observability/types";

import { ENV_ALL } from "../../Console/envScope";
import type { ObsEnvironment } from "../obsPlace";

export interface OrgSelection {
  /** The picked environments that exist, in the list's order. */
  names: string[];
  /** Their ids. Empty: no narrowing, every environment of the org. */
  ids: string[];
  /** The one picked environment's name, else ENV_ALL: what single-environment features (compare, alerts) key on. */
  env: string;
}

/** Picked names that the org no longer has are dropped rather than scoping the page to nothing. */
export function orgSelection(orgEnvironments: readonly ObsEnvironment[], picked: readonly string[] | undefined): OrgSelection {
  const chosen = new Set(picked ?? []);
  const named = orgEnvironments.filter((candidate) => chosen.has(candidate.name));
  return { names: named.map((candidate) => candidate.name), ids: named.map((candidate) => candidate.id), env: named.length === 1 ? named[0].name : ENV_ALL };
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
