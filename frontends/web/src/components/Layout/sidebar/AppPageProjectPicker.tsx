// Copyright (c) 2026 Reliant Labs

/**
 * An app page's project picker while the shell shows several projects
 * (U6b, owner 2026-11-20): the nav stays the same (Data is still Data), and
 * the page says which project's app it is showing, with the others one pick
 * away. It opens on the last project picked (useShellLastPick's focus), and a
 * pick becomes it.
 */

import { useMemo } from "react";
import { useNavigate } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";

import DropdownMenu, { type MenuGroup } from "@/components/forge-ui/dropdown_menu";

import { consolePath, type ConsolePage } from "../../Forge/Console/paths";
import { ProjectMark } from "../ProjectMark";
import { sectionInAnotherApp, sectionsAppOf, useShellLastPick, useShellSeveral } from "./shellProject";

export function AppPageProjectPicker({ app, page }: { app: string; page: ConsolePage }) {
  const navigate = useNavigate();
  const setFocus = useShellLastPick((state) => state.setFocus);
  const { mode: several, rows } = useShellSeveral();

  const options = useMemo(
    () =>
      rows.flatMap((row) => {
        const rowApp = sectionsAppOf(row);
        return rowApp ? [{ key: row.key, name: row.name, app: rowApp }] : [];
      }),
    [rows],
  );

  if (!several || options.length === 0) return null;
  const current = options.find((option) => option.app === app);
  const groups: MenuGroup[] = [
    {
      label: several === "all" ? "All projects" : `${options.length} projects`,
      selection: "single",
      items: options.map((option) => ({
        label: option.name,
        checked: option === current,
        testId: `app-page-project-${option.key}`,
        onClick: () => {
          setFocus(option.key);
          void navigate({ href: consolePath(option.app, sectionInAnotherApp(page)) });
        },
      })),
    },
  ];
  return (
    <DropdownMenu
      groups={groups}
      align="left"
      label="Show another project"
      density="compact"
      menuClassName="min-w-[220px] max-w-[300px]"
      trigger={(props) => (
        <button
          {...props}
          type="button"
          data-testid="app-page-project-picker"
          className="inline-flex h-8 max-w-[240px] items-center gap-1.5 rounded-md border border-border px-2 text-ui text-ink transition-colors hover:bg-ink/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ProjectMark name={current?.name ?? app} />
          <span className="min-w-0 truncate font-medium">{current?.name ?? app}</span>
          <ChevronDown className="size-3.5 shrink-0 text-ink-muted" aria-hidden="true" />
        </button>
      )}
    />
  );
}
