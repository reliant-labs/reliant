// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge — THE OVERVIEW.
 *
 * One row per environment Reliant records: where it runs, what it is on,
 * where that came from, and when it last moved. Below it, the environments
 * this project's CODE declares that Reliant has no record of yet.
 *
 * ── THE TABLE IS THE BACKEND, SO IT RENDERS DAEMON-OFFLINE ──────────────────
 *
 * `GetLiveView` is the table's only source (design §8.0, O-14). The
 * "not registered" list underneath is the one thing only the checkout knows,
 * so it comes from the daemon — labelled as such, and simply absent when the
 * daemon is asleep. It never mixes into the table: a row that exists because
 * a laptop is awake is not a row in the record.
 *
 * ── LOADING IS A SKELETON OF THE TABLE, NOT A SENTENCE ──────────────────────
 *
 * The page used to say "Reading this project's environments…" and then
 * replace the sentence with a table. The skeleton holds the table's shape so
 * nothing jumps when the rows land.
 */

import { useCallback, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { CircleDashed, Layers } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { useCloudEnvStatuses, useForgeRoster } from "@/hooks/forge-queries";
import { isPlacedKind } from "@/services/forge/live";
import type { RosterEnv } from "@/services/forge/roster";
import { useProjectStore } from "@/store/projectStore";

import { CloudNotice } from "../SourceNotices";
import { EnvironmentTable, type EnvironmentRow } from "./EnvironmentTable";

export function ForgeOverviewPage() {
  const navigate = useNavigate();
  const { project: projectParam } = useSearch({ from: "/_authenticated/_forge/forge" });
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = projectParam ?? currentProject?.id ?? null;

  const roster = useForgeRoster(projectId);
  const backendEnvs = useMemo(
    () => roster.envs.filter((env) => env.source === "backend" && env.live).map((env) => env.live!),
    [roster.envs]
  );
  const checkoutEnvs = useMemo(() => roster.envs.filter((env) => env.source === "checkout"), [roster.envs]);

  // GetStatus per PLACED environment. A self-managed env has no server-side
  // observer, so asking would answer for a cluster the platform has never
  // connected to; its row shows what the last render declared instead.
  const placedIds = useMemo(
    () => backendEnvs.filter((env) => isPlacedKind(env.kind)).map((env) => env.id),
    [backendEnvs]
  );
  const statuses = useCloudEnvStatuses(placedIds);

  const rows: EnvironmentRow[] = useMemo(
    () =>
      backendEnvs.map((env) => {
        const status = isPlacedKind(env.kind) ? statuses.get(env.id) : undefined;
        return { env, status: status?.data, statusLoading: !!status?.isLoading };
      }),
    [backendEnvs, statuses]
  );

  const openEnv = useCallback(
    (env: string) => {
      void navigate({
        to: "/forge/env/$env",
        params: { env },
        search: { project: projectId ?? undefined },
      });
    },
    [navigate, projectId]
  );

  const forgeProject = roster.projectName.name;
  const header = (
    <PageHeader
      title="Overview"
      subtitle={
        forgeProject ? (
          <>
            Every environment in <span className="font-mono">{forgeProject}</span>: where it runs,
            what it is on, and where that came from.
          </>
        ) : (
          "Every environment in this project: where it runs, what it is on, and where that came from."
        )
      }
    />
  );

  if (roster.isLoading || roster.resolvingName) {
    return (
      <div className="space-y-6" data-testid="forge-overview-loading">
        {header}
        <Card padding="none" aria-label="Loading environments" aria-busy="true">
          <SkeletonLoader variant="table-row" count={4} />
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-6" data-testid="forge-overview">
      {header}

      <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />

      {rows.length === 0 ? (
        <div data-testid="forge-overview-empty">
          <EmptyState
            icon={<Layers className="h-6 w-6" aria-hidden="true" />}
            title="No environments recorded yet"
            description={
              checkoutEnvs.length > 0
                ? "Your code declares the environments below. Open one and register it to track its releases and set its secrets here."
                : "Run forge env build <env> to record the first one, or open an environment your code declares and register it."
            }
          />
        </div>
      ) : (
        <EnvironmentTable rows={rows} onOpen={openEnv} />
      )}

      {checkoutEnvs.length > 0 && <UnregisteredEnvs envs={checkoutEnvs} onOpen={openEnv} />}
    </div>
  );
}

/**
 * Declared in the checkout, unknown to Reliant. From the daemon, and said so.
 * forge's own ledger facts (the release it last promoted from this machine)
 * are shown, because "not registered" is not "never deployed" — control-plane's
 * prod is on v1.7.15 by forge's file ledger with no row in Reliant.
 */
function UnregisteredEnvs({ envs, onOpen }: { envs: RosterEnv[]; onOpen: (env: string) => void }) {
  return (
    <Card padding="none" data-testid="forge-overview-unregistered">
      <CardHeader
        title="Not registered in Reliant"
        description="Declared in your checkout. Reliant has no record of these yet, so their history and secrets aren't tracked here. Read from your daemon."
      />
      <ul className="divide-y divide-border/60">
        {envs.map((env) => (
          <li key={env.name}>
            <button
              type="button"
              onClick={() => onOpen(env.name)}
              data-testid={`unregistered-${env.name}`}
              className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-muted/50 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
            >
              <CircleDashed className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="font-mono text-sm font-medium text-foreground">{env.name}</span>
              <span className="text-xs text-muted-foreground">
                {env.lifecycle === "local" ? "Local" : "Deployed"}
              </span>
              {env.forge?.release && (
                <span className="text-xs text-muted-foreground">
                  · forge ledger: <span className="font-mono text-foreground">{env.forge.release}</span>
                </span>
              )}
              <span className="ml-auto text-xs text-primary">Open</span>
            </button>
          </li>
        ))}
      </ul>
    </Card>
  );
}
