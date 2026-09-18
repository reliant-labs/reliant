// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/status.
 *
 * It resolves WHICH project to ask about the same way ForgeTopologyPage does —
 * from `projectStore.currentProject`, sending only the id, because the api-server
 * resolves that id to a path on the DAEMON's filesystem itself and the browser
 * has no business asserting a daemon-side path.
 *
 * WHICH ENVIRONMENT comes from the topology ledger rather than from a hardcoded
 * list or a free-text box. The declared environments are a fact forge already
 * reports, so reading them from the topology query means this page cannot offer
 * an env that does not exist, and cannot miss one a project just added. It is a
 * read of the existing cache in the common case: a user arriving from the
 * topology screen pays nothing for it.
 *
 * Env selection is component state rather than a URL search param. A param would
 * need a schema in routeSchemas.ts, and the panel is a read-only snapshot nobody
 * deep-links into a specific environment of; if that changes, a param is the
 * right answer and this is the place to add it.
 *
 * The audit strip sits ABOVE the panel and stays collapsed: per-project static
 * health is context for the env checks, not a competitor for attention.
 */

import { useEffect, useMemo, useState } from "react";

import { cn } from "@/lib/utils";
import { useProjectStore } from "@/store/projectStore";
import { useForgeAudit, useForgeEnvStatus, useForgeTopology } from "@/hooks/forge-queries";
import { environments } from "@/services/forge/topology";

import { AuditStrip } from "../Audit/AuditStrip";
import { EnvStatusPanel } from "./EnvStatusPanel";

/**
 * The env asked about before the ledger has loaded. Every forge project has a
 * `dev`, and asking about it immediately means the panel shows measured results
 * on first paint instead of an empty selector.
 */
const DEFAULT_ENV = "dev";

export function ForgeStatusPage() {
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id ?? null;

  const topology = useForgeTopology(projectId);
  const [selectedEnv, setSelectedEnv] = useState<string>(DEFAULT_ENV);

  /**
   * The environments forge declares. Only the `report` outcome carries any — for
   * every other outcome the list is empty and the selector hides itself, which is
   * correct: the panel below will be rendering that same outcome's explanation.
   */
  const envNames = useMemo(() => {
    if (topology.data?.kind !== "report") return [];
    return environments(topology.data.report).map((env) => env.env);
  }, [topology.data]);

  /**
   * If the ledger turns out not to declare the default, move to the first env it
   * does. Without this a project whose environments are named `staging`/`prod`
   * would sit on a `dev` that forge will answer about with nothing useful.
   */
  useEffect(() => {
    if (envNames.length === 0) return;
    if (!envNames.includes(selectedEnv)) setSelectedEnv(envNames[0]);
  }, [envNames, selectedEnv]);

  const status = useForgeEnvStatus(projectId, selectedEnv);
  const audit = useForgeAudit(projectId);

  if (!projectId) {
    return (
      <div
        data-testid="forge-status-no-project"
        className="mx-auto max-w-lg rounded-lg border border-dashed border-border px-6 py-12 text-center text-sm text-muted-foreground"
      >
        Select a project to see its forge runtime checks.
      </div>
    );
  }

  return (
    <div className="h-full space-y-4 overflow-y-auto bg-background p-6">
      <AuditStrip
        outcome={audit.data}
        isLoading={audit.isLoading}
        error={audit.error as Error | null}
        projectName={currentProject?.name}
      />

      {envNames.length > 0 && (
        <div
          data-testid="forge-status-env-picker"
          className="flex flex-wrap items-center gap-2"
          role="group"
          aria-label="Environment"
        >
          {envNames.map((env) => (
            <button
              key={env}
              type="button"
              onClick={() => setSelectedEnv(env)}
              aria-pressed={env === selectedEnv}
              className={cn(
                "rounded-md border px-2.5 py-1 font-mono text-xs",
                env === selectedEnv
                  ? "border-primary/40 bg-primary/10 text-foreground"
                  : "border-border text-muted-foreground hover:text-foreground"
              )}
            >
              {env}
            </button>
          ))}
        </div>
      )}

      <EnvStatusPanel
        outcome={status.data}
        isLoading={status.isLoading}
        error={status.error as Error | null}
        env={selectedEnv}
        projectName={currentProject?.name}
      />
    </div>
  );
}
