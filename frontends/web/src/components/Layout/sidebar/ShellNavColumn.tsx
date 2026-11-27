// Copyright (c) 2026 Reliant Labs

/**
 * The shell's nav column (U6): the org row on the nav's top bar, then Find
 * (the command palette), then the project(s) every row below is about, then
 * the nav itself, drawn from a config with the host's slots.
 *
 * It exists once. The app's nav (Layout/Sidebar.tsx) draws it with the app's
 * config (navConfig/hybrid.ts) and the chat slots, and hangs its context
 * menus and dialogs under it as children; another host (the public demo)
 * draws the same column with its own config and slots, and so does the phone's
 * drawer on `/m/*` (Layout/sidebar/MobileHybridNav), which also says where Find
 * goes and how its drawer closes. Which config and which slots are the only
 * things a host chooses, so the header, Find, the switcher and the renderer
 * never fork.
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
  /**
   * Find (the command palette) under the org row; on by default. A host with
   * no palette to open (the public demo: its keyboard host has no overlays)
   * leaves it out rather than draw a box that does nothing.
   */
  find?: boolean;
  /**
   * What Find opens, for a host with no keyboard host to run the palette (the
   * phone's drawer, Mobile/MobileNavDrawer: its Search screen). Omitted, the palette.
   */
  onFind?: () => void;
  /**
   * Runs after the project switcher's pick has navigated: closes the host's
   * drawer. Omitted, the narrow window's drawer closes (shell/AppShell.tsx).
   */
  onProjectPicked?: () => void;
  /** Runs after the org row switched to another org (the phone leaves the old org's chat). */
  onOrgSwitched?: (orgId: string) => void;
  /** Leaves a drill-in in place instead of navigating (NavRenderer `onBack`; the phone's drawer). */
  onBack?: () => void;
  /** Draws the top level on an area's route too, after `onBack` (NavRenderer `topLevel`). */
  topLevel?: boolean;
  /** Public hosts supply non-interactive scope chrome instead of live account/project controls. */
  header?: ReactNode;
  /** The column's root: the keyboard focuses it ("focus the sidebar"). */
  ref?: Ref<HTMLDivElement>;
  /** The host's own layers over the column: context menus, dialogs. */
  children?: ReactNode;
}

export function ShellNavColumn({
  config,
  slots,
  shell,
  pathname,
  search,
  onNavigate,
  flat,
  find = true,
  onFind,
  onProjectPicked,
  onOrgSwitched,
  onBack,
  topLevel,
  header,
  ref,
  children,
}: ShellNavColumnProps) {
  return (
    <div
      ref={ref}
      // The space under the host's own rows (AppShell's macOS title strip), not
      // h-full: 100% plus that strip pushed the footer (Settings) off a short window.
      className="flex min-h-0 flex-1 flex-col bg-background"
      data-onboarding="left-sidebar"
      // Marks the focus context for the keyboard dispatcher and gives the pane
      // something focusable, so "focus the chat list" has a target.
      data-context="left-sidebar"
      tabIndex={-1}
    >
      {/* The header (U6, owner): the org row on the nav's top bar, then Find (the command palette), then the project(s) every row below is about. */}
      {header === undefined ? <OrgHeaderRow onSwitched={onOrgSwitched} /> : header}
      {find && <FindBox onOpen={onFind} />}
      {header === undefined && <ProjectSwitcherRow shell={shell} pathname={pathname} onPicked={onProjectPicked} />}

      {/* The nav as data (U6c): the config's rows, or the area the route is in, then the footer.
          On a phone it is a drawer, where every row is drawn as a top-level row (NavRenderer `flat`). */}
      <ShellNavBody
        config={config}
        shell={shell}
        pathname={pathname}
        search={search}
        slots={slots}
        onNavigate={onNavigate}
        flat={flat}
        onBack={onBack}
        topLevel={topLevel}
      />

      {children}
    </div>
  );
}
