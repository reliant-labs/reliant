// Copyright (c) 2026 Reliant Labs

/**
 * The sidebar's header (U6, owner, after Vercel and Cloudflare), top to bottom:
 *
 *   [avatar] Org name [Plan] ⇕     the org, the row that anchors the nav (OrgHeaderRow)
 *   Find…                    F     the command palette (FindBox)
 *   [mark] Project ⌄               the project(s) every row below is about (ProjectSwitcherRow)
 *
 * The project row is the project selector (Projects/ProjectSelector.tsx), in
 * two panels:
 *
 *   one project   a click switches the whole nav to that project: on an
 *                 app's page, to the same section of its app; else to its
 *                 chats, or (an app with no code here) its Overview.
 *                 "All projects" and "Select several…" lead the list.
 *   several       choose N of M (the selector's multi-select,
 *                 hooks/useChatProjectFilter: `?projects=` and the
 *                 `sidebar.projects` setting). One left is that project;
 *                 none is every project.
 *
 * This is the app's one project filter: the chat list follows it, and has
 * no project filter of its own.
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
import { SIDEBAR_ORG_TRIGGER, SIDEBAR_PROJECT_TRIGGER, SidebarOrgLabel, SidebarOrgRow, SidebarProjectRow } from "./primitives";

/**
 * The org row: the org switcher at full width (avatar, name, plan, ⇕), on the
 * nav's 48px bar so its edge lines up with the page's. With no org (no control
 * plane, or a person in none) it is the brand, and opens nothing.
 */
export function OrgHeaderRow({ onSwitched }: { onSwitched?: (orgId: string) => void } = {}) {
  return (
    <SidebarOrgRow>
      <OrgSwitcher
        variant="sidebar-header"
        onSwitched={onSwitched}
        fallback={
          <div className={cn(SIDEBAR_ORG_TRIGGER, "h-9 px-1.5 hover:bg-transparent")} data-testid="org-header-brand">
            {isForgeShell() ? <ForgeBrand /> : <SidebarOrgLabel name="Reliant" />}
          </div>
        }
      />
    </SidebarOrgRow>
  );
}
import { navigateWithShellScope } from "../shellScopeNav";
import { useNavDrawer } from "../shell/shellFrame";
import { releaseNavDrawerHold } from "./navDrawerHold";
import { ALL_PROJECTS_PICK, sectionInAnotherApp, sectionsAppOf, useShellLastPick, type ShellProject } from "./shellProject";

/** A pick opens a page: the narrow window's drawer closes, as for any row of the nav (shell/AppShell.tsx). */
function closeNavDrawer(): void {
  releaseNavDrawerHold();
  useNavDrawer.getState().setOpen(false);
}

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
  onPicked = closeNavDrawer,
}: {
  shell: ShellProject;
  pathname: string;
  /** Closes the host's drawer after a pick (the phone's, Mobile/MobileNavDrawer); by default the narrow window's. */
  onPicked?: () => void;
}) {
  const setLastPick = useShellLastPick((state) => state.set);
  const rememberSeveral = useShellLastPick((state) => state.setSeveral);
  const filter = useChatProjectFilter();
  const [open, setOpen] = useState(false);
  const [several, setSeveral] = useState(false);

  const pickOne = useCallback(
    (selection: ProjectSelection) => {
      const { key, href } = pickTarget(selection, pathname);
      // Out of a choice of several, without the filter's own navigation.
      useChatProjectFilterStore.getState().setMany([]);
      setLastPick(key);
      // One project: the URL drops the several or All it carried (the page names its own).
      navigateWithShellScope(undefined, { to: href });
      onPicked();
    },
    [pathname, setLastPick, onPicked],
  );

  const pickAll = useCallback(() => {
    useChatProjectFilterStore.getState().setMany([]);
    setLastPick(ALL_PROJECTS_PICK);
    // `?projects=all`: a chat page shows every project's chats from the home; any other page stays.
    navigateWithShellScope({ mode: "all" }, chatShellRouteOf(pathname) ? { to: homePath() } : {});
    onPicked();
  }, [pathname, setLastPick, onPicked]);

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
      rememberSeveral(ids);
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
    <SidebarProjectRow testId="shell-switcher">
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
            className={SIDEBAR_PROJECT_TRIGGER}
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
            <ChevronDown className="ml-auto size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          </button>
        )}
      />
    </SidebarProjectRow>
  );
}
