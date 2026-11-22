// Copyright (c) 2026 Reliant Labs

/**
 * The landing of a project with nothing deployed (U6c): the way to deploy it,
 * and its chats. The console Overview's form for such an app
 * (Forge/Workloads/WorkloadPages.tsx), and a module of its own so the forge
 * demo's bundle reaches it without the Reliant project page
 * (ProjectHomePage.tsx); in the demo there is no Reliant project, so no chats.
 */

import { AppSectionEmptyState } from "../Forge/Console/AppSectionEmptyState";
import { ProjectChats } from "./ProjectChats";

/** The landing of a project with nothing deployed: its chats, and the one way to deploy it. Also the console Overview's form for such an app. */
export function NoAppLanding({ projectId, name, app }: { projectId: string | null; name: string; app: string | null }) {
  return (
    <div className="space-y-8" data-testid="project-landing" data-state="no-app">
      <div>
        <h1 className="text-title text-ink">Overview</h1>
        <p className="mt-1 text-ui text-ink-muted">{name} · no app deployed yet</p>
      </div>
      <div className="grid grid-cols-[minmax(0,1fr)] items-start gap-x-12 gap-y-8 xl:grid-cols-[minmax(0,1fr)_320px]">
        <div className="forge-ui -mt-16 min-w-0" data-testid="project-home-deploy">
          <AppSectionEmptyState page={{ kind: "overview" }} projectId={projectId} title={`${app ?? name} isn't deployed yet`} />
        </div>
        {projectId && <ProjectChats projectId={projectId} name={name} />}
      </div>
    </div>
  );
}
