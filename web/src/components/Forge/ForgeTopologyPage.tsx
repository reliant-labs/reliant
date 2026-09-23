// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/topology.
 *
 * All this does is wire the hooks to TopologyView. It does NOT resolve the
 * project — ForgeLayout does, because all three forge screens need the same
 * answer and only a parent can guarantee it has been reached before any child
 * renders. The layout also guarantees a project EXISTS by the time this mounts:
 * if resolution comes back empty it renders the project picker instead of the
 * outlet, which is why there is no no-project empty state here any more. That
 * state used to be a dead end — a sentence telling the user to select a project
 * on a page with nothing that could select one.
 *
 * Only the project's `id` is ever sent. The api-server resolves the id to a path
 * on the DAEMON's filesystem itself (enforcing that the caller owns the project),
 * which is why no path is passed from here: the browser has no business knowing,
 * or asserting, a daemon-side path.
 */

import { useCallback, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { useProjectStore } from "@/store/projectStore";
import { useForgeTopology, useVerifyForgeEnv } from "@/hooks/forge-queries";
import PageHeader from "@/components/forge-ui/page_header";

import { TopologyView } from "./TopologyView";

export function ForgeTopologyPage() {
  const navigate = useNavigate();
  const { project: projectParam } = useSearch({ from: "/_authenticated/_forge/forge/topology" });
  const currentProject = useProjectStore((state) => state.currentProject);

  // The URL is the source of truth once the layout has resolved it; the store is
  // the fallback for the tick before the param lands.
  const projectId = projectParam ?? currentProject?.id ?? null;

  const topology = useForgeTopology(projectId);
  const { verify, pendingEnv, lastOutcome } = useVerifyForgeEnv(projectId);

  /**
   * A verify that came back as anything other than a report leaves the row's
   * cells unverified, which is correct but silent — so the row says why. Note
   * the cells are NOT rewritten to `unreachable`: "nobody has looked" is the
   * true state of those cells, and claiming we looked and could not see is a
   * stronger assertion than this RPC supports for the whole row.
   */
  const verifyNotices = useMemo<Record<string, string>>(() => {
    if (!lastOutcome) return {};
    const { env, outcome } = lastOutcome;
    switch (outcome.kind) {
      case "unreachable":
        return {
          [env]: `Cluster unreachable, so ${env} is still unverified${
            outcome.meta.unreachableReason ? ` — ${outcome.meta.unreachableReason}` : ""
          }.`,
        };
      case "unsupported":
        return {
          [env]: `forge ${outcome.meta.forgeVersion} cannot verify this environment, so it is still unverified.`,
        };
      case "malformed":
        return { [env]: `forge's verify report could not be read, so ${env} is still unverified.` };
      case "not-forge-project":
        return { [env]: "This is no longer a forge project." };
      default:
        return {};
    }
  }, [lastOutcome]);

  /**
   * Clicking an environment opens that env's status screen. This is the single
   * change that most directly makes the surface navigable: the matrix rows were
   * inert, and status was reachable only by typing its URL. `env` is a search
   * param precisely so a row can link into it.
   */
  const onOpenEnv = useCallback(
    (env: string) => {
      void navigate({ to: "/forge/status", search: { project: projectId ?? undefined, env } });
    },
    [navigate, projectId]
  );

  return (
    // No padding or scroll container here: the forge shell (SidebarLayout) owns
    // both, and nesting a second scroller inside it produced a page that could
    // scroll in two places at once.
    <div className="space-y-6">
      <PageHeader
        title="Releases"
        subtitle="What each environment is promoted to, and whether the cluster agrees."
      />

      <TopologyView
        outcome={topology.data}
        isLoading={topology.isLoading}
        error={topology.error as Error | null}
        onVerify={verify}
        verifyingEnv={pendingEnv}
        verifyNotices={verifyNotices}
        projectName={currentProject?.name}
        projectId={projectId}
        onOpenEnv={onOpenEnv}
      />
    </div>
  );
}
