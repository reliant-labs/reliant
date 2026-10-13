// Copyright (c) 2025 Reliant Labs

/**
 * WHERE the Observability panel is, and which environments that means.
 *
 * Environment is a filter, not a place (owner decision 1), but the panel does
 * live in two places: the project's area, where the environment filter is the
 * reader's to set, and an env page's tab, where it is pinned.
 */

import type { ObsSearch } from "./obsSearch";

export interface ObsEnvironment {
  /** control-plane environment id. */
  id: string;
  name: string;
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
    };

/** The scope's environment ids: the pinned one, or the picked names resolved (empty = every environment). */
export function envIdsOf(place: ObsPlace, picked: readonly string[] | undefined): string[] {
  if (place.kind === "env") return [place.env.id];
  const byName = new Map(place.environments.map((env) => [env.name, env.id]));
  return (picked ?? []).flatMap((name) => {
    const id = byName.get(name);
    return id ? [id] : [];
  });
}
