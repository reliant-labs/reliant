// Copyright (c) 2025 Reliant Labs

/**
 * The Releases pages as console routes: /forge/p/:project/releases and
 * /forge/p/:project/releases/:version.
 *
 * This file only adapts the console to the pages: the project and scope from
 * the console context, the environments from its roster, the URL's search
 * state, and the promote flow. ReleasesList and ReleaseDetail hold the pages
 * themselves, and know nothing about routing — which is what lets the preview
 * and the tests mount them on fixtures.
 *
 * Promoting goes through the shared plan flow (usePromoteFlow): forge plans
 * the promotion against the checkout, the reader sees the diff — a move to an
 * older release labelled as a rollback — and only the confirmed plan applies.
 * That plan needs a daemon on the project's checkout, so a console opened on
 * a project no Reliant project here declares can read releases but not
 * promote them, and says so.
 */

import { useCallback, useMemo } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";

import { useForgeConsole } from "../Console/consoleContext";
import { consolePath, withSearch } from "../Console/paths";
import { CloudNotice } from "../SourceNotices";
import { DeployRunsPanel } from "../Deploy/DeployRuns";
import { ENV_ALL } from "../Console/envScope";
import { usePromoteFlow } from "../Promote/usePromoteFlow";
import { usePromoterNames } from "./useReleaseData";
import { ReleaseDetail, type ReleaseDetailSearch } from "./ReleaseDetail";
import { ReleasesList, type ReleasesSearch } from "./ReleasesList";
import type { ReleaseEnv } from "./releaseModel";

/** The roster's recorded, deployed environments, as the Releases pages need them: prod first, previews apart. */
function useReleaseEnvs(): { envs: ReleaseEnv[]; loading: boolean } {
  const { roster } = useForgeConsole();
  const envs = useMemo(
    () =>
      roster.envs
        .flatMap((candidate): ReleaseEnv[] => {
          const live = candidate.live;
          // A local environment runs the working tree: it has no releases.
          if (!live || live.kind === "local") return [];
          return [
            {
              id: live.id,
              name: candidate.name,
              kind: live.kind === "preview" ? "preview" : "long",
              current: live.release,
              promotedAt: live.promotedAt,
              promotedByActor: live.promotedByActor,
              promotedByUserId: live.promotedByUserId,
            },
          ];
        })
        .sort((a, b) => (a.name === "prod" ? -1 : b.name === "prod" ? 1 : a.name.localeCompare(b.name))),
    [roster.envs]
  );
  return { envs, loading: roster.isLoading || roster.resolvingName };
}

/** Merge `next` into the current search, dropping cleared keys, without a history entry. */
function useSearchUpdater<T extends object>() {
  const navigate = useNavigate();
  return useCallback(
    (next: Partial<T>) =>
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, ...next }),
        replace: true,
      }),
    [navigate]
  );
}

export function ReleasesPage() {
  const { forgeProject, env, roster, href } = useForgeConsole();
  const search = useSearch({ strict: false }) as ReleasesSearch;
  const onSearch = useSearchUpdater<ReleasesSearch>();
  const { envs, loading } = useReleaseEnvs();
  const { control, dialog } = usePromoteFlow();
  const names = usePromoterNames(forgeProject);
  const releaseHref = useCallback(
    (version: string, extra?: Record<string, unknown>) => href({ kind: "release", version }, extra),
    [href]
  );
  return (
    <div className="space-y-4" data-testid="console-releases">
      <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
      <ReleasesList
        project={forgeProject}
        env={env}
        envs={envs}
        envsLoading={loading}
        search={search}
        onSearch={onSearch}
        releaseHref={releaseHref}
        promote={control}
        scopeIsLocal={roster.envs.some((candidate) => candidate.name === env && candidate.lifecycle === "local")}
      />
      <DeployRunsPanel
        project={forgeProject}
        env={env === ENV_ALL ? null : env}
        names={names}
        hrefs={{ release: (version) => href({ kind: "release", version }) }}
        openId={search.deploy ?? null}
      />
      {dialog}
    </div>
  );
}

export function ReleasePage() {
  const { forgeProject, env, roster, href } = useForgeConsole();
  const { version = "" } = useParams({ strict: false }) as { version?: string };
  const search = useSearch({ strict: false }) as ReleaseDetailSearch;
  const onSearch = useSearchUpdater<ReleaseDetailSearch>();
  const { envs, loading } = useReleaseEnvs();
  const { control, dialog } = usePromoteFlow();
  const releaseHref = useCallback(
    (other: string, extra?: Record<string, unknown>) => href({ kind: "release", version: other }, extra),
    [href]
  );
  const activityHref = useCallback(
    (name: string, target: string) => withSearch(consolePath(forgeProject, { kind: "activity" }), { env: name, target }),
    [forgeProject]
  );
  return (
    <div className="space-y-4" data-testid="console-release">
      <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
      <ReleaseDetail
        // A new release is a new page: nothing (an open dialog, a picked tab's local state) carries over.
        key={version}
        project={forgeProject}
        version={version}
        env={env}
        envs={envs}
        envsLoading={loading}
        search={search}
        onSearch={onSearch}
        listHref={href({ kind: "releases" })}
        releaseHref={releaseHref}
        activityHref={activityHref}
        promote={control}
      />
      {dialog}
    </div>
  );
}
