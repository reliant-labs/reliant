// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge — THE OVERVIEW.
 *
 * It replaces three screens that were each built around one forge command —
 * Releases (topology), Environments (env status) and Status (verify + audit)
 * — and overlapped almost entirely, because the question a reader brings is
 * per ENVIRONMENT: what environments does this project have, where does each
 * run, what is it on, is it healthy. So: the project audit strip, then one
 * row per environment. Everything deeper is on that environment's page.
 *
 * TWO SOURCES, NEITHER WAITED ON (see services/forge/environments.ts):
 *
 *   the daemon         forge's topology — every env the checkout declares,
 *                      its binding and image digests. The audit, too.
 *   the control plane  the envs it holds for this forge project, and — for
 *                      the ones it RUNS — their health and bound release,
 *                      straight from DeployService. No daemon.
 *
 * A daemon that is asleep leaves the control plane's rows fully rendered, and
 * says so in one line naming what is missing. Only when NEITHER source has an
 * environment does the page fall back to forge's own full-panel answers ("not
 * a forge project", "your forge is too old") — those are facts about the
 * project, and a project with cloud environments visibly IS a forge project.
 */

import { useCallback, useMemo, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import PageHeader from "@/components/forge-ui/page_header";
import {
  cloudRunIds,
  useCloudEnvStatuses,
  useForgeAudit,
  useForgeEnvironments,
} from "@/hooks/forge-queries";
import { cloudRunIdOf, envFacts } from "@/services/forge/environments";
import { useProjectStore } from "@/store/projectStore";

import { AuditStrip } from "../Audit/AuditStrip";
import { DeployDialog } from "../Deploy/DeployDialog";
import { ForgeMalformed, ForgeUnreachable, ForgeUnsupported, NotForgeProject } from "../ForgeStates";
import { PromoteDialog } from "../Promote/PromoteDialog";
import { CloudNotice, DaemonOfflineNotice } from "../SourceNotices";
import { EnvironmentTable, type EnvironmentRow } from "./EnvironmentTable";

export function ForgeOverviewPage() {
  const navigate = useNavigate();
  const { project: projectParam } = useSearch({ from: "/_authenticated/_forge/forge" });
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = projectParam ?? currentProject?.id ?? null;

  const state = useForgeEnvironments(projectId);
  const { envs, topology, daemon, cloud, projectName } = state;
  const audit = useForgeAudit(projectId);

  const statuses = useCloudEnvStatuses(useMemo(() => cloudRunIds(envs), [envs]));

  const rows: EnvironmentRow[] = useMemo(
    () =>
      envs.map((summary) => {
        const id = cloudRunIdOf(summary);
        const status = id ? statuses.get(id) : undefined;
        return { summary, facts: envFacts(summary, status?.data, status?.isLoading) };
      }),
    [envs, statuses]
  );

  const report = topology.data?.kind === "report" ? topology.data.report : null;

  // Promote / deploy are dialogs keyed by env, mounted only while open so a
  // closed dialog holds no plan and therefore no confirmation token.
  const [promoteEnv, setPromoteEnv] = useState<string | null>(null);
  const [deployEnv, setDeployEnv] = useState<string | null>(null);

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

  const header = (
    <PageHeader
      title="Overview"
      subtitle={
        projectName.name ? (
          <>
            Every environment in <span className="font-mono">{projectName.name}</span>: where it runs,
            what it is on, and whether it is healthy.
          </>
        ) : (
          "Every environment in this project: where it runs, what it is on, and whether it is healthy."
        )
      }
    />
  );

  // Nothing from either side yet.
  if (state.isLoading) {
    return (
      <div className="space-y-6">
        {header}
        <p data-testid="forge-overview-loading" className="text-sm text-muted-foreground">
          Reading this project&apos;s environments…
        </p>
      </div>
    );
  }

  // forge's own answers about the PROJECT, and only when the control plane has
  // nothing to show either — otherwise they would hide environments that exist.
  if (envs.length === 0 && topology.data && topology.data.kind !== "report") {
    const outcome = topology.data;
    return (
      <div className="space-y-6">
        {header}
        {outcome.kind === "not-forge-project" ? (
          <NotForgeProject projectName={currentProject?.name} />
        ) : outcome.kind === "unsupported" ? (
          <ForgeUnsupported meta={outcome.meta} />
        ) : outcome.kind === "unreachable" ? (
          <ForgeUnreachable meta={outcome.meta} />
        ) : (
          <ForgeMalformed meta={outcome.meta} />
        )}
      </div>
    );
  }

  const daemonError = topology.error as Error | null;

  return (
    <div className="space-y-6" data-testid="forge-overview">
      {header}

      {daemon === "offline" ? (
        <DaemonOfflineNotice
          scope={
            projectName.name
              ? "Reliant cloud environments are read from the control plane and shown below; local and cluster environments, the project audit, and promote/deploy need the daemon."
              : "This project's forge name has not been read from its forge.yaml by a daemon yet, so its Reliant cloud environments cannot be looked up either. Start your daemon once to see them."
          }
          detail={daemonError?.message}
        />
      ) : (
        <AuditStrip
          outcome={audit.data}
          isLoading={audit.isLoading}
          error={audit.error as Error | null}
          projectName={currentProject?.name}
        />
      )}

      <CloudNotice availability={cloud.data?.availability} detail={cloud.data?.detail} />

      {rows.length === 0 ? (
        <div
          data-testid="forge-overview-empty"
          className="rounded-lg border border-dashed border-border px-6 py-12 text-center text-sm text-muted-foreground"
        >
          {daemon === "offline"
            ? "No environments to show until your daemon answers."
            : "This forge project declares no environments yet."}
        </div>
      ) : (
        <EnvironmentTable
          rows={rows}
          promoteRelease={report?.latest_release ?? null}
          // Both flows plan on the daemon (forge computes the plan and holds
          // the guard), so with no daemon neither is offered.
          canShip={daemon === "ok" && !!projectId}
          onOpen={openEnv}
          onPromote={setPromoteEnv}
          onDeploy={setDeployEnv}
        />
      )}

      {report && (
        <p className="text-xs text-muted-foreground">
          {report.latest_release ? (
            <>
              Latest release in this checkout:{" "}
              <span className="font-mono text-foreground">{report.latest_release}</span>
              {typeof report.releases?.length === "number" && ` · ${report.releases.length} cut`}.{" "}
            </>
          ) : null}
          Timestamps are <span className="text-foreground">promote</span> times, not deploy times —
          promotion writes a pointer, deployment moves bytes.
        </p>
      )}

      {promoteEnv && report?.latest_release && (
        <PromoteDialog
          isOpen
          onClose={() => setPromoteEnv(null)}
          projectId={projectId}
          env={promoteEnv}
          release={report.latest_release}
          projectName={currentProject?.name}
        />
      )}
      {deployEnv && (
        <DeployDialog
          isOpen
          onClose={() => setDeployEnv(null)}
          projectId={projectId}
          env={deployEnv}
          projectName={currentProject?.name}
        />
      )}
    </div>
  );
}
