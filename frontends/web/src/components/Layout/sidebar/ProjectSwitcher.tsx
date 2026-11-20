// Copyright (c) 2026 Reliant Labs

/**
 * The project shell's top row (U6b, prototype): `[Org ▾] / [Project ▾]`.
 * The org half is the org switcher; the project half is the project
 * selector (Projects/ProjectSelector.tsx), and picking in it moves the whole
 * nav to that project:
 *
 *   on an app's page, to the same section of the new project's app;
 *   else to the project's chats, or (an app with no code here) its Overview.
 *
 * "All projects" opens the Projects page.
 */

import { useMemo } from "react";
import { AppWindow, ChevronDown, LayoutGrid } from "lucide-react";

import { projectChatsPath, projectsPath } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { appRowKey } from "@/services/projects/catalog";

import { consolePageOf, consolePath } from "../../Forge/Console/paths";
import { ForgeBrand } from "../../ForgeShell/ForgeBrand";
import { OrgSwitcher } from "../../Orgs/OrgSwitcher";
import { ProjectSelector, type ProjectSelection, type ProjectSelectorLeadItem } from "../../Projects/ProjectSelector";
import { isForgeShell } from "../../../lib/shell";
import { ProjectMark } from "../ProjectMark";
import { SIDEBAR_ORG_TRIGGER, SidebarOrgLabel, SidebarOrgRow } from "./primitives";
import { deployedAppOf, sectionInAnotherApp, useShellLastPick, type ShellProject } from "./shellProject";

/** Where picking `selection` goes from `pathname`, and the key it becomes. */
export function pickTarget(selection: ProjectSelection, pathname: string): { key: string; href: string } {
  const key = selection.kind === "project" ? selection.row.key : (selection.project?.key ?? appRowKey(selection.app.name));
  const app = selection.kind === "project" ? deployedAppOf(selection.row) : selection.app.name;
  const projectId = selection.kind === "project" ? selection.row.project.id : (selection.project?.project.id ?? null);
  const onConsole = consolePageOf(pathname);
  if (onConsole && app) return { key, href: consolePath(app, sectionInAnotherApp(onConsole.page)) };
  if (projectId) return { key, href: projectChatsPath(projectId) };
  return { key, href: consolePath(app ?? "", { kind: "overview" }) };
}

export function ProjectSwitcherRow({
  shell,
  pathname,
  onNavigate,
}: {
  shell: ShellProject;
  pathname: string;
  onNavigate: (href: string) => void;
}) {
  const setLastPick = useShellLastPick((state) => state.set);
  const leadItems = useMemo<ProjectSelectorLeadItem[]>(
    () => [
      {
        key: "all-projects",
        label: "All projects",
        icon: <LayoutGrid className="size-4 text-muted-foreground" aria-hidden="true" />,
        onSelect: () => onNavigate(projectsPath()),
        testId: "shell-switcher-all-projects",
      },
    ],
    [onNavigate],
  );
  const appOnly = shell.row?.kind === "app" || (!shell.projectId && !!shell.name);

  return (
    <SidebarOrgRow>
      <div className="flex min-w-0 flex-1 items-center gap-0.5" data-testid="shell-switcher">
        <div className="flex min-w-0 max-w-[45%] shrink-0">
          <OrgSwitcher
            variant="sidebar-switcher"
            fallback={
              <div className={cn(SIDEBAR_ORG_TRIGGER, "hover:bg-transparent")}>
                {isForgeShell() ? <ForgeBrand /> : <SidebarOrgLabel name="Reliant" />}
              </div>
            }
          />
        </div>
        <span aria-hidden="true" className="shrink-0 select-none text-ui text-muted-foreground">
          /
        </span>
        <ProjectSelector
          mode="navigate"
          selectedKey={shell.key}
          leadItems={leadItems}
          align="left"
          placeholder="Switch project…"
          onSelect={(selection) => {
            const { key, href } = pickTarget(selection, pathname);
            setLastPick(key);
            onNavigate(href);
          }}
          trigger={(props) => (
            <button
              {...props}
              type="button"
              data-testid="shell-project-switcher"
              aria-label={shell.name ? `Project: ${shell.name}. Switch project` : "Choose a project"}
              className={cn(SIDEBAR_ORG_TRIGGER, "gap-1.5 px-1.5")}
            >
              {appOnly ? (
                <AppWindow className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              ) : (
                <ProjectMark name={shell.name ?? "?"} className="size-5 text-meta" />
              )}
              <span className="min-w-0 truncate font-semibold" data-testid="shell-project-name">
                {shell.name ?? "Choose a project"}
              </span>
              <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
            </button>
          )}
        />
      </div>
    </SidebarOrgRow>
  );
}
