// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/p/$project/environments — CONFIGURE ›
 * ENVIRONMENTS: every environment of the project, as configured objects.
 * Each row opens that environment's page (lifecycle, register, deploy and
 * promote, domain bindings, the local dev stack).
 *
 * This was the console's Overview while the nav was the environment roster.
 * Environment is the top bar's scope now (decision D1), so this list is the
 * one page that does not narrow to it: it is where the scope's choices are
 * configured.
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
 * The skeleton holds the table's shape so nothing jumps when the rows land.
 */

import { useCallback, useMemo } from "react";
import { useNavigate } from "@tanstack/react-router";
import { CircleDashed, Layers } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import { DOCS, GettingStarted } from "@/components/Forge/GettingStarted";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { useCloudEnvStatuses } from "@/hooks/forge-queries";
import { isPlacedKind, type LiveEnv } from "@/services/forge/live";
import type { RosterEnv } from "@/services/forge/roster";

import { useForgeConsole } from "../Console/consoleContext";
import { CloudNotice } from "../SourceNotices";
import { EnvironmentTable, type EnvironmentRow } from "./EnvironmentTable";

/**
 * The table's rows for `envs`, with the platform's observation for each
 * PLACED environment. A self-managed env has no server-side observer, so
 * asking would answer for a cluster the platform has never connected to; its
 * row shows what the last render declared instead.
 */
export function useEnvironmentRows(envs: LiveEnv[]): EnvironmentRow[] {
  const placedIds = useMemo(() => envs.filter((env) => isPlacedKind(env.kind)).map((env) => env.id), [envs]);
  const statuses = useCloudEnvStatuses(placedIds);
  return useMemo(
    () =>
      envs.map((env) => {
        const status = isPlacedKind(env.kind) ? statuses.get(env.id) : undefined;
        return { env, status: status?.data, statusLoading: !!status?.isLoading };
      }),
    [envs, statuses]
  );
}

export function EnvironmentsPage() {
  const navigate = useNavigate();
  const { roster, href } = useForgeConsole();
  const backendEnvs = useMemo(
    () => roster.envs.filter((env) => env.source === "backend" && env.live).map((env) => env.live!),
    [roster.envs]
  );
  const checkoutEnvs = useMemo(() => roster.envs.filter((env) => env.source === "checkout"), [roster.envs]);
  const rows = useEnvironmentRows(backendEnvs);

  const openEnv = useCallback(
    (env: string) => void navigate({ href: href({ kind: "environment", env }) }),
    [navigate, href]
  );

  const header = (
    <PageHeader
      title="Environments"
      meta={<span>Where each one runs, what it is on, and where that came from</span>}
      className="mb-0"
    />
  );

  if (roster.isLoading || roster.resolvingName) {
    return (
      <div className="space-y-6" data-testid="forge-environments-loading">
        {header}
        <Card padding="none" aria-label="Loading environments" aria-busy="true">
          <SkeletonLoader variant="table-row" count={4} />
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-6" data-testid="forge-environments">
      {header}

      <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />

      {rows.length === 0 ? (
        <div data-testid="forge-environments-empty">
          <GettingStarted
            testId="environments-getting-started"
            icon={<Layers className="h-4 w-4" />}
            title="No environments recorded yet"
            about="Environments lists where this project runs: each environment's host, the release it is on, where that came from, and when it last moved."
            steps={[
              {
                text: "Deploy one through Reliant from your checkout. That records the environment and its first release.",
                command: "forge env deploy <env>",
              },
              {
                text:
                  checkoutEnvs.length > 0
                    ? "Or open one of the environments your code declares, below, and register it to set its secrets before the first deploy."
                    : "Or open an environment your code declares and register it to set its secrets before the first deploy.",
              },
            ]}
            docs={{ href: DOCS.deploys, label: "How environments and deploys work" }}
          >
            <p>A local environment (forge env up) runs your working tree on your machine; it has no releases and needs no registering.</p>
          </GettingStarted>
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
      <div className="px-4 pt-3">
        <CardHeader
          title="Not registered in Reliant"
          description="Declared in your checkout. Reliant has no record of these yet, so their history and secrets aren't tracked here. Read from your daemon."
        />
      </div>
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
              {/* Only a lifecycle someone actually stated. Without a control-
                  plane row or forge's runtime answer, "deployed" would be a
                  guess — dev's destination is `mixed`, same as prod's. */}
              {env.lifecycle === "local" && <span className="text-xs text-muted-foreground">Runs locally</span>}
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
