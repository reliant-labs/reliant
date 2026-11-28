// Copyright (c) 2025 Reliant Labs

/**
 * ProjectService: Alex's two projects (shop, the forge project; docs, plain
 * git), and the org's project catalog (data/catalog.json) that the project
 * selector, the Projects page and an app's Code card read: eight projects and
 * one app with no code here, app chips of every link kind (SOURCE, NAME,
 * NOT_HOSTED), a static site, a shared project, machines with and without the
 * code. shop records where it was deployed from (DEMO_REPO at v1.8.4's
 * commit), so the app console's Code card has a source to show. Both of
 * Alex's projects are checked out on the cloud machine, where their chats run.
 */
import { Code, ConnectError } from "@connectrpc/connect";

import { ListProjectsWithAppsResponseSchema, ProjectInstallState, ProjectService, type ListProjectsWithAppsResponse } from "@/gen/reliant/v1/project_pb";

import catalogJson from "../../data/catalog.json?raw";
import { packJson } from "../../packJson";
import { DAY, HOUR } from "../../shopStory";
import { DAEMON_IDS, DAEMON_NAMES, isoAgo, projects } from "../../workspace";
import { serve } from "../serve";

/** The org's catalog, on the pack clock. */
export const projectCatalog = (): ListProjectsWithAppsResponse => packJson(ListProjectsWithAppsResponseSchema, catalogJson);

export function projectHandlers() {
  return serve(ProjectService, {
    listProjects: () => {
      const list = projects();
      return { projects: list, total: list.length };
    },
    getProject: (req) => {
      // Alex's own two, then any project the catalog lists (a project's page opened from the selector).
      const project = projects().find((p) => p.id === req.projectId) ?? projectCatalog().projects.find((row) => row.project?.id === req.projectId)?.project;
      if (!project) throw new ConnectError("project not found", Code.NotFound);
      return { project };
    },
    listProjectsWithApps: () => projectCatalog(),
    listProjectDaemons: () => ({
      projectDaemons: projects().map((p) => ({
        projectId: p.id, daemonId: DAEMON_IDS.cloud, path: p.path, defaultBranch: p.defaultBranch, clonedAt: isoAgo(12 * DAY),
        installState: ProjectInstallState.INSTALLED, installError: "", verifiedAt: isoAgo(3 * HOUR), daemonName: DAEMON_NAMES.cloud, nestedRepos: [],
      })),
    }),
  });
}
