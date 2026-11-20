// Copyright (c) 2026 Reliant Labs

/**
 * The project shell's top row (U6b, prototype): `[Org ▾] / [Project ▾]`.
 * The org half is the org switcher; the project half is the project
 * selector (Projects/ProjectSelector.tsx), in two panels:
 *
 *   one project   a click switches the whole nav to that project: on an
 *                 app's page, to the same section of its app; else to its
 *                 chats, or (an app with no code here) its Overview.
 *                 "All projects" and "Select several…" lead the list.
 *   several       choose N of M (the selector's multi-select, the chat
 *                 list's filter of #3027: `?projects=` and the
 *                 `sidebar.projects` setting). One left is that project;
 *                 none is every project.
 */

import { useCallback, useMemo, useState } from "react";
import { AppWindow, ChevronDown, Layers, LayoutGrid, ListChecks } from "lucide-react";

import { useChatProjectFilter, useChatProjectFilterStore } from "@/hooks/useChatProjectFilter";
import { chatShellRouteOf } from "@/lib/chatRoute";
import { homePath, projectChatsPath } from "@/lib/paths";
import { cn } from "@/lib/utils";
import { appRowKey } from "@/services/projects/catalog";

import { consolePageOf, consolePath } from "../../Forge/Console/paths";
import { ForgeBrand } from "../../ForgeShell/ForgeBrand";
import { OrgSwitcher } from "../../Orgs/OrgSwitcher";
import { ProjectSelector, type ProjectSelection, type ProjectSelectorLeadItem } from "../../Projects/ProjectSelector";
import { isForgeShell } from "../../../lib/shell";
import { ProjectMark } from "../ProjectMark";
import { SIDEBAR_ORG_TRIGGER, SidebarOrgLabel, SidebarOrgRow } from "./primitives";
import { ALL_PROJECTS_PICK, SEVERAL_PROJECTS_PICK, sectionInAnotherApp, sectionsAppOf, useShellLastPick, type ShellProject } from "./shellProject";

/** Where picking `selection` goes from `pathname`, and the key it becomes. */
export function pickTarget(selection: ProjectSelection, pathname: string): { key: string; href: string } {
  const key = selection.kind === "project" ? selection.row.key : (selection.project?.key ?? appRowKey(selection.app.name));
  const app = selection.kind === "project" ? sectionsAppOf(selection.row) : selection.app.name;
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
  const filter = useChatProjectFilter();
  const [open, setOpen] = useState(false);
  const [several, setSeveral] = useState(false);

  const pickOne = useCallback(
    (selection: ProjectSelection) => {
      const { key, href } = pickTarget(selection, pathname);
      // Out of a choice of several, without the filter's own navigation.
      useChatProjectFilterStore.getState().setMany([]);
      setLastPick(key);
      onNavigate(href);
    },
    [pathname, setLastPick, onNavigate],
  );

  const pickAll = useCallback(() => {
    useChatProjectFilterStore.getState().setMany([]);
    setLastPick(ALL_PROJECTS_PICK);
    // A chat page shows every project's chats from the home; any other page stays.
    if (chatShellRouteOf(pathname)) onNavigate(homePath());
  }, [pathname, setLastPick, onNavigate]);

  const leadItems = useMemo<ProjectSelectorLeadItem[]>(
    () => [
      {
        key: "all-projects",
        label: "All projects",
        icon: <LayoutGrid className="size-4 text-muted-foreground" aria-hidden="true" />,
        selected: shell.mode === "all",
        onSelect: pickAll,
        testId: "shell-switcher-all-projects",
      },
      {
        key: "select-several",
        label: "Select several projects…",
        icon: <ListChecks className="size-4 text-muted-foreground" aria-hidden="true" />,
        onSelect: () => {
          setSeveral(true);
          // The pick closed the panel: open it again, choosing several.
          requestAnimationFrame(() => setOpen(true));
        },
        testId: "shell-switcher-select-several",
      },
    ],
    [shell.mode, pickAll],
  );

  const chosen = shell.mode === "many" ? shell.ids : shell.mode === "one" && shell.projectId ? [shell.projectId] : [];
  const onChangeSeveral = (ids: string[]) => {
    if (ids.length >= 2) {
      filter.set(ids);
      setLastPick(SEVERAL_PROJECTS_PICK);
      return;
    }
    if (ids.length === 1) {
      const row = shell.catalog?.rows.find((candidate) => candidate.kind === "project" && candidate.project.id === ids[0]);
      if (row?.kind === "project") pickOne({ kind: "project", row });
      return;
    }
    pickAll();
  };

  const appOnly = shell.mode === "one" && (shell.row?.kind === "app" || (!shell.projectId && !!shell.name));
  const label =
    shell.mode === "all" ? "All projects" : shell.mode === "many" ? `${shell.ids.length} projects` : (shell.name ?? "Choose a project");

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
          open={open}
          onOpenChange={(next) => {
            setOpen(next);
            // Closing ends a choice of several; the next open starts on one project.
            if (!next) setSeveral(false);
          }}
          {...(several
            ? {
                mode: "chat" as const,
                multiple: { selected: chosen, onChange: onChangeSeveral },
                placeholder: "Choose projects…",
              }
            : {
                mode: "navigate" as const,
                selectedKey: shell.mode === "one" ? shell.key : null,
                leadItems,
                placeholder: "Switch project…",
                onSelect: pickOne,
              })}
          align="left"
          trigger={(props) => (
            <button
              {...props}
              type="button"
              data-testid="shell-project-switcher"
              data-mode={shell.mode}
              aria-label={`Project: ${label}. Switch project`}
              className={cn(SIDEBAR_ORG_TRIGGER, "gap-1.5 px-1.5")}
            >
              {shell.mode !== "one" ? (
                <Layers className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              ) : appOnly ? (
                <AppWindow className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              ) : (
                <ProjectMark name={shell.name ?? "?"} className="size-5 text-meta" />
              )}
              <span className="min-w-0 truncate font-semibold" data-testid="shell-project-name">
                {label}
              </span>
              <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
            </button>
          )}
        />
      </div>
    </SidebarOrgRow>
  );
}
