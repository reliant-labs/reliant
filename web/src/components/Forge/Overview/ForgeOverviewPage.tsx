// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge — THE OVERVIEW.
 *
 * One row per environment: where it runs, what it is on, where that came
 * from, and when it last moved. Everything deeper is on the environment's
 * page.
 *
 * ── THE LIST COMES FROM THE CONTROL PLANE, SO IT RENDERS DAEMON-OFFLINE ─────
 *
 * `GetLiveView` is the only source here (design §8.0, O-14). The page used to
 * join a daemon topology report with a control-plane list, which meant an
 * asleep laptop blanked rows describing production environments the control
 * plane was observing the whole time — and told the user to "start your daemon
 * once to see them", which was the tool asking for a favour to show facts it
 * already had.
 *
 * So there is no daemon hook on this page at all. The one thing the checkout
 * knows and the control plane does not — an environment declared only in the
 * KCL — belongs to PREVIEW, which labels it "would be created" and offers to
 * register it. Nothing on this list pretends to know about an environment
 * nothing has ever recorded.
 *
 * The project AUDIT moved to Preview for the same reason: it is forge's static
 * analysis over files on the user's disk, so it was never something this page
 * could show without a daemon.
 */

import { useCallback, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import PageHeader from "@/components/forge-ui/page_header";
import { useCloudEnvStatuses, useLiveView } from "@/hooks/forge-queries";
import { isPlacedKind } from "@/services/forge/live";
import { useProjectStore, type Project } from "@/store/projectStore";

import { CloudNotice } from "../SourceNotices";
import { EnvironmentTable, type EnvironmentRow } from "./EnvironmentTable";

export function ForgeOverviewPage() {
  const navigate = useNavigate();
  const { project: projectParam } = useSearch({ from: "/_authenticated/_forge/forge" });
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = projectParam ?? currentProject?.id ?? null;
  const forgeProject = useProjectStore((state) => persistedForgeProjectName(state, projectId));

  const live = useLiveView(forgeProject);
  // Memoised so the `?? []` fallback does not allocate a fresh array on every
  // render and re-run both useMemos below it.
  const envs = useMemo(() => live.data?.envs ?? [], [live.data]);

  // GetStatus per PLACED environment. A self-managed env has no server-side
  // observer, so asking would answer for a cluster the platform has never
  // connected to; its row shows what the last render declared instead.
  const placedIds = useMemo(
    () => envs.filter((env) => isPlacedKind(env.kind)).map((env) => env.id),
    [envs]
  );
  const statuses = useCloudEnvStatuses(placedIds);

  const rows: EnvironmentRow[] = useMemo(
    () =>
      envs.map((env) => {
        const status = isPlacedKind(env.kind) ? statuses.get(env.id) : undefined;
        return { env, status: status?.data, statusLoading: !!status?.isLoading };
      }),
    [envs, statuses]
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

  const openPreview = useCallback(
    (env: string) => {
      void navigate({
        to: "/forge/env/$env",
        params: { env },
        search: { project: projectId ?? undefined, tab: "preview" },
      });
    },
    [navigate, projectId]
  );

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

  if (live.isLoading && !live.data) {
    return (
      <div className="space-y-6">
        {header}
        <p data-testid="forge-overview-loading" className="text-sm text-muted-foreground">
          Reading this project&apos;s environments…
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-6" data-testid="forge-overview">
      {header}

      <CloudNotice availability={live.data?.availability} detail={live.data?.detail} />

      {rows.length === 0 ? (
        <div
          data-testid="forge-overview-empty"
          className="space-y-2 rounded-lg border border-dashed border-border px-6 py-12 text-center text-sm text-muted-foreground"
        >
          {/* Not an error, and not a request to go and start something. A
              project with no recorded environment is a project nobody has
              built yet, and the remedy is a command. */}
          <p>No environments have been built yet.</p>
          <p className="text-xs">
            <code className="font-mono text-foreground">forge env build &lt;env&gt;</code> records
            the first one, or open an environment&apos;s Preview to register it.
          </p>
        </div>
      ) : (
        <EnvironmentTable rows={rows} onOpen={openEnv} onPreview={openPreview} />
      )}

      <p className="text-xs text-muted-foreground">
        Timestamps are <span className="text-foreground">promote</span> times, not deploy times —
        promotion writes a pointer, deployment moves bytes.
      </p>
    </div>
  );
}

/** The forge project name persisted on `projectId`'s row, from whichever store slot holds it. */
function persistedForgeProjectName(
  state: { projects: Project[]; currentProject: Project | null },
  projectId: string | null | undefined
): string | null {
  if (!projectId) return null;
  const row =
    state.currentProject?.id === projectId
      ? state.currentProject
      : state.projects.find((project) => project.id === projectId);
  return row?.forge_project_name ?? null;
}
