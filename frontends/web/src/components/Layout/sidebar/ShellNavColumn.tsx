// Copyright (c) 2026 Reliant Labs

/**
 * The shell's nav column (U6): the org row on the nav's top bar, then Find
 * (the command palette), then the project(s) every row below is about, then
 * the nav itself, drawn from a config with the host's slots.
 *
 * It exists once. The app's nav (Layout/Sidebar.tsx) draws it with the app's
 * config (navConfig/hybrid.ts) and the chat slots, and hangs its context
 * menus and dialogs under it as children; another host (the public demo)
 * draws the same column with its own config and slots. Which config and which
 * slots are the only things a host chooses, so the header, Find, the switcher
 * and the renderer never fork.
 */

import type { ReactNode, Ref } from "react";

import { FindBox } from "./FindBox";
import type { NavConfig } from "./navConfig/types";
import type { NavigateOptions, NavSlots } from "./NavRenderer";
import { OrgHeaderRow, ProjectSwitcherRow } from "./ProjectSwitcher";
import { ShellNavBody } from "./ShellNavBody";
import type { ShellProject } from "./shellProject";

export interface ShellNavColumnProps {
  /** The nav to draw (navConfig/types.ts): the app's `hybridNav`, or a host's own. */
  config: NavConfig;
  /** What the slots in `config` render (the chat list, New chat, Inbox, Help…); a slot with no entry draws nothing. */
  slots: NavSlots;
  /** The project shell's scope (sidebar/shellProject.ts `useShellProject`). */
  shell: ShellProject;
  /** The page on screen and its search. */
  pathname: string;
  search: Record<string, unknown>;
  /** Opens a page; `drill` keeps a drawer open (NavRenderer). */
  onNavigate: (href: string, options?: NavigateOptions) => void;
  /** Every row drawn as a top-level row: a phone-sized window's drawer (NavRenderer `flat`). */
  flat?: boolean;
  /** The column's root: the keyboard focuses it ("focus the sidebar"). */
  ref?: Ref<HTMLDivElement>;
  /** The host's own layers over the column: context menus, dialogs. */
  children?: ReactNode;
}

export function ShellNavColumn({ config, slots, shell, pathname, search, onNavigate, flat, ref, children }: ShellNavColumnProps) {
  return (
    <div
      ref={ref}
      className="flex h-full flex-col bg-background"
      data-onboarding="left-sidebar"
      // Marks the focus context for the keyboard dispatcher and gives the pane
      // something focusable, so "focus the chat list" has a target.
      data-context="left-sidebar"
      tabIndex={-1}
    >
      {/* The header (U6, owner): the org row on the nav's top bar, then Find (the command palette), then the project(s) every row below is about. */}
      <OrgHeaderRow />
      <FindBox />
      <ProjectSwitcherRow shell={shell} pathname={pathname} />

      {/* The nav as data (U6c): the config's rows, or the area the route is in, then the footer.
          On a phone it is a drawer, where every row is drawn as a top-level row (NavRenderer `flat`). */}
      <ShellNavBody config={config} shell={shell} pathname={pathname} search={search} slots={slots} onNavigate={onNavigate} flat={flat} />

      {children}
    </div>
  );
}
