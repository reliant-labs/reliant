// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/status.
 *
 * It does NOT resolve the project — ForgeLayout does, for all three forge
 * screens, and it guarantees one exists by the time this mounts (rendering a
 * picker rather than the outlet when resolution comes back empty). That is why
 * there is no no-project empty state here any more: the old one was a dead end,
 * a sentence asking the user to select a project on a page offering no way to.
 * Only the project's `id` is sent; the api-server resolves it to a path on the
 * DAEMON's filesystem and enforces that the caller owns it.
 *
 * WHICH ENVIRONMENT comes from the topology ledger rather than from a hardcoded
 * list or a free-text box. The declared environments are a fact forge already
 * reports, so reading them from the topology query means this page cannot offer
 * an env that does not exist, and cannot miss one a project just added. It is a
 * read of the existing cache in the common case: a user arriving from the
 * topology screen pays nothing for it.
 *
 * Env selection is a URL search param, NOT component state. The previous comment
 * here argued that a param was unnecessary because "the panel is a read-only
 * snapshot nobody deep-links into a specific environment of" — that stopped
 * being true the moment topology rows became links into this screen. It is also
 * what makes a refresh keep the selection instead of silently resetting to dev.
 *
 * The audit strip sits ABOVE the panel and stays collapsed: per-project static
 * health is context for the env checks, not a competitor for attention.
 */

import { useCallback, useEffect, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { useProjectStore } from "@/store/projectStore";
import { useForgeAudit, useForgeEnvStatus, useForgeTopology } from "@/hooks/forge-queries";
import { environments } from "@/services/forge/topology";
import PageHeader from "@/components/forge-ui/page_header";

import { AuditStrip } from "../Audit/AuditStrip";
import { EnvTabs } from "../EnvTabs";
import { EnvStatusPanel } from "./EnvStatusPanel";

/**
 * The env asked about before the ledger has loaded, and before the URL names
 * one. Every forge project has a `dev`, so asking about it immediately means the
 * panel shows measured results on first paint instead of an empty selector.
 */
const DEFAULT_ENV = "dev";

export function ForgeStatusPage() {
  const navigate = useNavigate();
  const { project: projectParam, env: envParam } = useSearch({
    from: "/_authenticated/_forge/forge/status",
  });
  const currentProject = useProjectStore((state) => state.currentProject);

  // The URL is the source of truth once the layout has resolved it; the store is
  // the fallback for the tick before the param lands.
  const projectId = projectParam ?? currentProject?.id ?? null;

  const topology = useForgeTopology(projectId);
  const selectedEnv = envParam ?? DEFAULT_ENV;

  const selectEnv = useCallback(
    (env: string) => {
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, env }),
        replace: true,
      });
    },
    [navigate]
  );

  /**
   * The environments forge declares. Only the `report` outcome carries any — for
   * every other outcome the list is empty and the tab strip hides itself, which
   * is correct: the panel below will be rendering that same outcome's
   * explanation.
   */
  const envNames = useMemo(() => {
    if (topology.data?.kind !== "report") return [];
    return environments(topology.data.report).map((env) => env.env);
  }, [topology.data]);

  /**
   * If the ledger turns out not to declare the selected env, move to the first
   * one it does. Without this a project whose environments are named
   * `staging`/`prod` would sit on a `dev` that forge will answer about with
   * nothing useful — and now that the env comes from the URL, the same applies
   * to a stale or hand-typed `?env=`.
   */
  useEffect(() => {
    if (envNames.length === 0) return;
    if (!envNames.includes(selectedEnv)) selectEnv(envNames[0]);
  }, [envNames, selectedEnv, selectEnv]);

  const status = useForgeEnvStatus(projectId, selectedEnv);
  const audit = useForgeAudit(projectId);

  return (
    // No padding or scroll container here: the forge shell (SidebarLayout) owns
    // both, and nesting a second scroller inside it produced a page that could
    // scroll in two places at once.
    <div className="space-y-6">
      <PageHeader
        title="Status"
        subtitle="Runtime checks forge ran against this environment, and static checks against the project."
      />

      <AuditStrip
        outcome={audit.data}
        isLoading={audit.isLoading}
        error={audit.error as Error | null}
        projectName={currentProject?.name}
      />

      <EnvTabs
        envs={envNames}
        selected={selectedEnv}
        onSelect={selectEnv}
        isLoading={topology.isLoading}
      />

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
