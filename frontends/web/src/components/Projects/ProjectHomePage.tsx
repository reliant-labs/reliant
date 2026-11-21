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
 * NoAppLanding (NoAppLanding.tsx) stays the console Overview's form for an
 * app with nothing deployed (WorkloadPages OverviewPage).
 */

import { useEffect } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";

import { consolePath } from "../Forge/Console/paths";
import { deployedAppOf, rowForRef } from "../Layout/sidebar/shellProject";
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
