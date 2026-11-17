// Copyright (c) 2025 Reliant Labs

/**
 * WHERE A FORGE APP'S CODE LIVES, when Reliant has no project for it.
 *
 * "Ask Reliant" needs a checkout, and a forge app deployed from someone's own
 * computer (standalone forge, CI) has none in Reliant. The control plane does
 * record the git remote a deploy was rendered from: each workload's declared
 * spec carries its bundle's `sourceRepo`. When that is a GitHub repository,
 * "Add a project" and "Add a machine" can offer it already filled in, so
 * getting the code is a confirmation rather than a search.
 *
 * Best effort: a site with no running workload, a deploy from a folder with no
 * remote, or a remote off GitHub records nothing usable, and the reader picks
 * the repository themselves, as before.
 */

import { useMemo } from "react";

import { useObservabilityOrg, useWorkloads } from "@/hooks/observability-queries";
import type { GitRepo } from "@/services/controlPlane/git";
import type { ObsScope, Workload } from "@/services/controlPlane/observability/types";

import { githubRepoOf } from "../../Workloads/workloadModel";

/** The GitHub repository the first workload with a recorded source was deployed from. */
export function sourceRepoOf(workloads: readonly Pick<Workload, "declared">[]): GitRepo | null {
  for (const workload of workloads) {
    const fullName = workload.declared ? githubRepoOf(workload.declared.sourceRepo) : null;
    if (!fullName) continue;
    return {
      fullName,
      cloneUrl: `https://github.com/${fullName}.git`,
      // The default branch is not recorded; the clone forms default to it as well.
      defaultBranch: "main",
      description: "",
      private: false,
      language: "",
      updatedAt: "",
    };
  }
  return null;
}

/** The forge app's GitHub repository, once read; null while reading or when none is recorded. */
export function useForgeSourceRepo(forgeProject: string | undefined): GitRepo | null {
  const { orgId } = useObservabilityOrg();
  const name = forgeProject?.trim() ?? "";
  const scope = useMemo<ObsScope | null>(() => (orgId && name ? { orgId, projectId: name, envIds: [], filters: [] } : null), [orgId, name]);
  const workloads = useWorkloads(scope, "7d");
  return useMemo(() => sourceRepoOf(workloads.data?.workloads ?? []), [workloads.data]);
}
