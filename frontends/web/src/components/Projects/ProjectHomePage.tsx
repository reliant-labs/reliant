// Copyright (c) 2026 Reliant Labs

/**
 * `/projects/<id>` (U6c: Home and Overview are ONE landing page, "Overview").
 *
 *   a deployed app   → its console's Overview (consolePath overview), which is
 *                      the landing: health, what needs attention (alerts,
 *                      errors, chats waiting), the live state, recent releases
 *                      and the project's chats. Replace, so Back skips it.
 *   no deployed app  → the project's own page (U7, ProjectPage): the way to
 *                      deploy it, its apps, code and machines, its chats, and
 *                      its Workspaces and Settings tabs.
 *
 * NoAppLanding stays the console Overview's form for an app with nothing
 * deployed (WorkloadPages OverviewPage).
 */

import { useEffect } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";

import { AppSectionEmptyState } from "../Forge/Console/AppSectionEmptyState";
import { consolePath } from "../Forge/Console/paths";
import { deployedAppOf, rowForRef } from "../Layout/sidebar/shellProject";
import { ProjectChats } from "./ProjectChats";
import { ProjectPage } from "./ProjectPage";
import { useSelectorCatalog } from "./ProjectSelector";

export function ProjectHomePage() {
  const { projectId = "" } = useParams({ strict: false }) as { projectId?: string };
  const { catalog } = useSelectorCatalog(undefined);
  const row = rowForRef(catalog, { kind: "project", id: projectId });
  const deployed = deployedAppOf(row);
  const navigate = useNavigate();
  useEffect(() => {
    if (deployed) void navigate({ href: consolePath(deployed, { kind: "overview" }), replace: true });
  }, [deployed, navigate]);
  // Until the catalog answers, nothing: the landing must not flash the project's page before redirecting.
  if (deployed || !catalog) return null;
  return <ProjectPage />;
}

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
