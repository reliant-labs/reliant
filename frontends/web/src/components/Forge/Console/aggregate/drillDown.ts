// Copyright (c) 2026 Reliant Labs

/**
 * Drilling down from an aggregate page into one project: the same page of
 * that project's app, with the shell switched to that one project. The URL
 * drops the several it carried (`?projects=`), so the page there is the
 * one-project page, not the aggregate again.
 */

import { useCallback } from "react";

import { navigateWithShellScope } from "../../../Layout/shellScopeNav";
import { useShellLastPick } from "../../../Layout/sidebar/shellProject";
import { consolePath, type ConsolePage } from "../paths";
import type { SeveralApp } from "../severalProjects";

/** Where `project`'s row of an aggregate of `page` leads: the same page of its app. */
export function drillDownHref(project: SeveralApp, page: ConsolePage): string {
  return consolePath(project.app, page);
}

/** Opens `page` for `project` alone: the shell's one project, its params dropped (a push, so Back returns). */
export function useDrillDown(): (project: SeveralApp, page: ConsolePage) => void {
  const setLastPick = useShellLastPick((state) => state.set);
  return useCallback(
    (project: SeveralApp, page: ConsolePage) => {
      setLastPick(project.key);
      navigateWithShellScope(undefined, { to: drillDownHref(project, page) });
    },
    [setLastPick],
  );
}

/** Whether a click is a plain one the page should handle, not a new tab or window. */
export function isPlainClick(event: { metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean; button: number }): boolean {
  return !(event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0);
}
