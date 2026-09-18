// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/secrets.
 *
 * Same shape as ForgeTopologyPage: resolve WHICH project from
 * projectStore.currentProject — the same source every project-scoped screen uses
 * — and send only its id. The api-server resolves the id to a path on the
 * DAEMON's filesystem and enforces that the caller owns the project, which is
 * why no path is passed from here.
 *
 * The environment list comes from the topology report rather than a constant.
 * Topology already enumerates every environment forge knows about, so asking for
 * it costs one cached query and cannot go stale against a project that renames
 * or adds one. The selected env is component state rather than a search param:
 * it is a view preference with no deep-link value, and keeping it out of the URL
 * keeps the route to the same three lines as the topology route.
 */

import { useEffect, useMemo, useState } from "react";

import { useProjectStore } from "@/store/projectStore";
import { useForgeSecrets, useForgeTopology } from "@/hooks/forge-queries";
import { environments } from "@/services/forge/topology";

import { SecretsView } from "./SecretsView";

export function ForgeSecretsPage() {
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id ?? null;

  const topology = useForgeTopology(projectId);
  const [selectedEnv, setSelectedEnv] = useState<string | null>(null);

  const envNames = useMemo(
    () =>
      topology.data?.kind === "report"
        ? environments(topology.data.report).map((env) => env.env)
        : [],
    [topology.data]
  );

  // Settle on the first environment once the list is known, and re-settle if the
  // current selection disappears (a different project, or an env removed from
  // the checkout). Nothing is guessed before the list arrives — a default of
  // "dev" would ask the daemon about an environment that may not exist.
  useEffect(() => {
    if (envNames.length === 0) return;
    if (selectedEnv && envNames.includes(selectedEnv)) return;
    setSelectedEnv(envNames[0]);
  }, [envNames, selectedEnv]);

  const secrets = useForgeSecrets(projectId, selectedEnv);

  if (!projectId) {
    return (
      <div
        data-testid="forge-secrets-no-project"
        className="mx-auto max-w-lg rounded-lg border border-dashed border-border px-6 py-12 text-center text-sm text-muted-foreground"
      >
        Select a project to see which secrets its environments declare.
      </div>
    );
  }

  return (
    <div className="h-full overflow-y-auto bg-background p-6">
      <SecretsView
        outcome={secrets.data}
        isLoading={secrets.isLoading}
        error={secrets.error as Error | null}
        topology={topology.data}
        topologyLoading={topology.isLoading}
        selectedEnv={selectedEnv}
        onSelectEnv={setSelectedEnv}
        projectName={currentProject?.name}
      />
    </div>
  );
}
