// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/topology.
 *
 * All this does is resolve WHICH project to ask about and wire the hooks to
 * TopologyView. The project comes from `projectStore.currentProject` — the same
 * source every other project-scoped screen uses, kept in sync with the URL by
 * App's `/project/$projectId` param — and only its `id` is sent. The api-server
 * resolves the id to a path on the DAEMON's filesystem itself (enforcing that the
 * caller owns the project), which is why no path is passed from here: the browser
 * has no business knowing, or asserting, a daemon-side path.
 */

import { useMemo } from "react";

import { useProjectStore } from "@/store/projectStore";
import { useForgeTopology, useVerifyForgeEnv } from "@/hooks/forge-queries";

import { TopologyView } from "./TopologyView";

export function ForgeTopologyPage() {
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id ?? null;

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

  if (!projectId) {
    return (
      <div
        data-testid="forge-no-project"
        className="mx-auto max-w-lg rounded-lg border border-dashed border-border px-6 py-12 text-center text-sm text-muted-foreground"
      >
        Select a project to see its forge release topology.
      </div>
    );
  }

  return (
    <div className="h-full overflow-y-auto bg-background p-6">
      <TopologyView
        outcome={topology.data}
        isLoading={topology.isLoading}
        error={topology.error as Error | null}
        onVerify={verify}
        verifyingEnv={pendingEnv}
        verifyNotices={verifyNotices}
        projectName={currentProject?.name}
        projectId={projectId}
      />
    </div>
  );
}
