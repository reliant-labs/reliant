// Copyright (c) 2026 Reliant Labs

/**
 * `/projects/<id>` (U6c: Home and Overview are ONE landing page, "Overview").
 *
 *   a deployed app   → its console's Overview (consolePath overview), the
 *                      landing. Replace, so Back skips it.
 *   no deployed app  → the landing's no-app form, here: the project's chats
 *                      and the way to deploy it (NoAppLanding).
 */

import { useEffect } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";

import { AppSectionEmptyState } from "../Forge/Console/AppSectionEmptyState";
import { consolePath } from "../Forge/Console/paths";
import { deployedAppOf, rowForRef, sectionsAppOf } from "../Layout/sidebar/shellProject";
import { ProjectChats } from "./ProjectChats";
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
  // Until the catalog answers, nothing: the landing must not flash the no-app form before redirecting.
  if (deployed || !catalog) return null;
  return (
    <div className="h-full overflow-y-auto" data-testid="project-home">
      <div className="mx-auto max-w-[var(--page-max)] px-8 pb-12 pt-8 max-md:px-4">
        <NoAppLanding projectId={projectId} name={row?.name ?? "Project"} app={sectionsAppOf(row)} />
      </div>
    </div>
  );
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
