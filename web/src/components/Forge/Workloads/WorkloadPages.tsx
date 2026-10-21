// Copyright (c) 2025 Reliant Labs

/**
 * The Overview and a workload as console routes: /forge/p/:project and
 * /forge/p/:project/workloads/:env/:name.
 *
 * This file only adapts the console to the pages: the project, the scope and
 * the environments from the console context, the URL's search state, and the
 * restart. OverviewHome and WorkloadDetail hold the pages themselves and know
 * nothing about routing — which is what lets the preview and the tests mount
 * them on fixtures.
 */

import { useCallback, useMemo } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { isPlacedKind } from "@/services/forge/live";
import { restartWorkload, type RestartedWorkload } from "@/services/forge/cloudEnvs";

import { useForgeConsole } from "../Console/consoleContext";
import { consolePath, withSearch } from "../Console/paths";
import { CloudNotice } from "../SourceNotices";
import { usePromoterNames } from "../Releases/useReleaseData";
import { promoterOf } from "../Releases/releaseModel";
import { OverviewHome, type OverviewHrefs, type OverviewSearch } from "./OverviewHome";
import { WorkloadDetail, type RestartControl, type WorkloadDetailSearch } from "./WorkloadDetail";
import type { StripEnv } from "./workloadModel";

/** The roster's recorded, deployed environments as the Overview's strip needs them: prod first, previews last. */
function useStripEnvs(): { envs: StripEnv[]; loading: boolean } {
  const { roster, forgeProject } = useForgeConsole();
  const names = usePromoterNames(forgeProject);
  const envs = useMemo(
    () =>
      roster.envs
        .flatMap((candidate): StripEnv[] => {
          const live = candidate.live;
          // A local environment runs on a developer's machine: nothing is deployed to watch.
          if (!live || live.kind === "local") return [];
          return [
            {
              id: live.id,
              name: candidate.name,
              release: live.release,
              promotedAt: live.promotedAt,
              promotedBy: promoterOf(live, names),
              preview: live.kind === "preview",
            },
          ];
        })
        .sort((a, b) => Number(a.preview) - Number(b.preview) || (a.name === "prod" ? -1 : b.name === "prod" ? 1 : a.name.localeCompare(b.name))),
    [roster.envs, names]
  );
  return { envs, loading: roster.isLoading || roster.resolvingName };
}

/** Merge `next` into the current search, dropping cleared keys, without a history entry. */
function useSearchUpdater<T extends object>() {
  const navigate = useNavigate();
  return useCallback(
    (next: Partial<T>, options: { replace?: boolean } = { replace: true }) =>
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, ...next }),
        replace: options.replace,
      }),
    [navigate]
  );
}

export function OverviewPage() {
  const { forgeProject, env, setEnv, roster, href } = useForgeConsole();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as OverviewSearch;
  const onSearch = useSearchUpdater<OverviewSearch>();
  const { envs, loading } = useStripEnvs();
  const hrefs = useMemo<OverviewHrefs>(
    () => ({
      // The workload page carries the window, so its charts open on the one the reader was looking at.
      workload: (name, envName) => href({ kind: "workload", env: envName, name }, { range: search.range }),
      release: (version) => href({ kind: "release", version }),
      releases: href({ kind: "releases" }),
      alerts: href({ kind: "obs-view", view: "alerts" }),
      activity: href({ kind: "activity" }),
    }),
    [href, search.range]
  );
  return (
    <div className="space-y-4" data-testid="console-overview">
      <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
      <OverviewHome
        project={forgeProject}
        env={env}
        envs={envs}
        envsLoading={loading}
        search={search}
        onSearch={onSearch}
        hrefs={hrefs}
        onSelectEnv={setEnv}
        navigateTo={(target) => void navigate({ href: target })}
      />
    </div>
  );
}

export function WorkloadPage() {
  const { forgeProject, roster, href } = useForgeConsole();
  const { env: envName = "", name = "" } = useParams({ strict: false }) as { env?: string; name?: string };
  const search = useSearch({ strict: false }) as WorkloadDetailSearch & { env?: string };
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const scoped = roster.envs.find((candidate) => candidate.name === envName) ?? null;
  const live = scoped?.live ?? null;

  const onSearch = useCallback(
    (next: WorkloadDetailSearch, options: { replace?: boolean } = {}) =>
      void navigate({
        to: ".",
        // A tab switch REPLACES the explorer keys (WorkloadDetail passes the full set); the scope param rides along.
        search: (prev: Record<string, unknown>) => ({ env: prev.env as string | undefined, ...next }),
        replace: options.replace,
      }),
    [navigate]
  );

  const restart = useMutation<RestartedWorkload, Error, void>({
    mutationFn: () => restartWorkload(live!.id, name),
    // The pods roll over the next minute: re-read the workload so the Pods table follows.
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["observability"] }),
  });
  const restartControl = useMemo<RestartControl>(
    () => ({
      onRestart: () => restart.mutateAsync(),
      unavailableReason: !live
        ? null
        : isPlacedKind(live.kind)
          ? null
          : `Restart is offered for environments Reliant hosts. ${envName} runs on your own cluster, so restart it there (kubectl rollout restart).`,
    }),
    [restart, live, envName]
  );

  const hrefs = useMemo(
    () => ({
      overview: withSearch(consolePath(forgeProject, { kind: "overview" }), { env: envName }),
      release: (version: string) => href({ kind: "release", version }),
      activity: withSearch(consolePath(forgeProject, { kind: "activity" }), { env: envName, target: `workload:${name}` }),
      dashboards: href({ kind: "obs-view", view: "dashboards" }),
    }),
    [forgeProject, envName, name, href]
  );

  if (roster.isLoading || roster.resolvingName) {
    return <div data-testid="console-workload" data-state="loading" className="h-40 animate-pulse rounded-lg border border-border bg-surface motion-reduce:animate-none" />;
  }
  if (!live) {
    return (
      <div className="space-y-4" data-testid="console-workload">
        <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
        <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted" data-testid="workload-unrecorded-env">
          {scoped
            ? `Reliant has no record of ${envName} as a deployed environment, so ${name} has no telemetry or actions here.`
            : `There is no environment named ${envName} in this project.`}
        </p>
      </div>
    );
  }
  const { env: _scope, ...detailSearch } = search;
  return (
    <div className="space-y-4" data-testid="console-workload">
      <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
      <WorkloadDetail
        // Another workload is another page: an open dialog or popover never carries over.
        key={`${envName}/${name}`}
        project={forgeProject}
        env={{ id: live.id, name: envName }}
        name={name}
        search={detailSearch}
        onSearch={onSearch}
        hrefs={hrefs}
        restart={restartControl}
      />
    </div>
  );
}
