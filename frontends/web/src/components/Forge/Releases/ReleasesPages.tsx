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
 * Promoting goes through the existing plan flow (PromoteDialog): forge plans
 * the promotion against the checkout, the reader sees the diff — a move to an
 * older release labelled as a rollback — and only the confirmed plan applies.
 * That plan needs a daemon on the project's checkout, so a console opened on
 * a project no Reliant project here declares can read releases but not
 * promote them, and says so.
 */

import { useCallback, useMemo, useState } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";

import { useProject } from "@/hooks/project-queries";

import { useForgeConsole } from "../Console/consoleContext";
import { consolePath, withSearch } from "../Console/paths";
import { CloudNotice } from "../SourceNotices";
import { PromoteDialog } from "../Promote/PromoteDialog";
import { ReleaseDetail, type ReleaseDetailSearch } from "./ReleaseDetail";
import { ReleasesList, type PromoteControl, type PromoteRequest, type ReleasesSearch } from "./ReleasesList";
import { shortVersion, type ReleaseEnv } from "./releaseModel";

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

/** The promote flow: the request the reader made, and the plan dialog that carries it out. */
function usePromote(): { control: PromoteControl; dialog: React.ReactNode } {
  const { projectId } = useForgeConsole();
  const projectName = useProject(projectId)?.name;
  const [request, setRequest] = useState<PromoteRequest | null>(null);
  const control = useMemo<PromoteControl>(
    () => ({
      onPromote: setRequest,
      blockedReason: projectId
        ? null
        : "Promoting plans the change against this project's checkout. Open the project in Reliant, with its daemon running, to promote from here.",
    }),
    [projectId]
  );
  const dialog =
    request && projectId ? (
      <PromoteDialog
        isOpen
        onClose={() => setRequest(null)}
        projectId={projectId}
        env={request.env}
        release={request.version}
        projectName={projectName}
        title={
          request.intent === "rollback"
            ? `Roll back ${request.env} to ${shortVersion(request.version)}`
            : `Promote ${shortVersion(request.version)} to ${request.env}`
        }
      />
    ) : null;
  return { control, dialog };
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
  const { forgeProject, env, setEnv, roster, href } = useForgeConsole();
  const search = useSearch({ strict: false }) as ReleasesSearch;
  const onSearch = useSearchUpdater<ReleasesSearch>();
  const { envs, loading } = useReleaseEnvs();
  const { control, dialog } = usePromote();
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
        onSelectEnv={setEnv}
        promote={control}
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
  const { control, dialog } = usePromote();
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
