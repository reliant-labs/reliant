// Copyright (c) 2026 Reliant Labs

/**
 * THE NAV AS DATA (U6d, prototype): the owner's chosen hybrid nav
 * (hybrid.ts) is a plain list of these items over the shell's pages.
 *
 *   row      a link: its href is built from the context, `match` says whether
 *            it is the page on screen
 *   group    an accordion (Workflows ▾): its rows open in place; clicking an
 *            open one closes it, and the group holding the page on screen
 *            opens on every navigation
 *   area     a drill-in (Agents ›, Observability ›): on any route
 *            the area's `match` claims, the area's rows REPLACE the nav under a
 *            "‹ Area" back header. Its row links to its first page, so being
 *            "in" an area is always a URL (Back, refresh and share restore it)
 *   heading  a quiet label over the rows after it
 *   slot     a live widget the shell renders (the chat list, New chat, the
 *            Inbox bell): data says where it goes, the shell says what it is
 *
 * A config's `footer` holds the rows under the nav's hairline (Usage &
 * billing, Help, Settings). `sectionPages` are pages whose own section list takes the
 * nav column on their routes (Settings), reached by a plain link, not a drill-in.
 */

import type { ReactNode } from "react";

import type { ConsolePage, OrgPage } from "../../../Forge/Console/paths";
import type { GroupedNavInput } from "../../../Forge/Console/consoleNavModel";
import type { ShellMode } from "../shellProject";

/** Everything an item's href, match or availability reads: the route and the shell's scope. */
export interface NavCtx {
  /** The page on screen (either form, `/x` or `/o/<org>/x`). */
  pathname: string;
  search: Record<string, unknown>;
  /** One project, several, or every one (sidebar/shellProject.ts). */
  mode: ShellMode;
  /** The focused code project, when there is one. */
  projectId: string | null;
  projectName: string | null;
  /** The app the project's pages open (shellProject `app`); null with no project. */
  app: string | null;
  /** The app's console model (alerts, hub, env scope, registered rows); null with no app. */
  console: GroupedNavInput | null;
  /** The console page on screen, when it is one. */
  page: ConsolePage | null;
  /** The org console page on screen (org-wide Observability), when it is one. */
  orgPage: OrgPage | null;
}

export type NavHref = (ctx: NavCtx) => string | undefined;
export type NavMatch = (ctx: NavCtx) => boolean;
export type NavChildren = readonly NavItem[] | ((ctx: NavCtx) => readonly NavItem[]);

/** What the shell renders for a slot. */
export type NavSlotId =
  /** New chat (or Set up chats while chats cannot run). */
  | "new-chat"
  /** The chat list: grouped by branch, or by project across several. */
  | "chat-list"
  /** The Inbox row, with its count. */
  | "inbox"
  /** A row's trail: the live automations pill (Workflows). */
  | "workflows-pill"
  /** A row's trail: a dot while a chat waits on the person (Agents). */
  | "chats-need-you"
  /** A row's control: the desktop app's update button (Settings). */
  | "update-button"
  /** The footer's Help row: a tour, the docs, keyboard shortcuts (sidebar/HelpMenu). */
  | "help"
  /** A host's own actions in the footer: the public demo's tour, Install Forge and telemetry notice in a phone's drawer (Forge/Demo/demoNavConfig.ts). */
  | "host-actions";

/**
 * A count, a dot or a word after a row's label. A `label` is a stage the
 * page is in (Experiments: "Beta"), not something asking for attention: a
 * group draws only its own badge, so a closed group never carries one up.
 */
export type NavBadge =
  | { type: "count"; value: number; tone?: "danger"; label?: string }
  | { type: "dot"; label: string }
  | { type: "label"; text: string };
/**
 * A badge after an item's label: a row's, a group's (drawn open or closed)
 * or an area's. It reads the context only, so it is live from whatever the
 * nav already holds: `ctx.console?.navCounts?.<source>` for a console count
 * (Forge/Console/consoleNavModel.ts), `ctx.console?.alertsFiring`, … Undefined:
 * no badge. See countBadge (navConfig/model.ts) for the usual count.
 */
export type NavBadgeOf = (ctx: NavCtx) => NavBadge | undefined;

interface NavItemBase {
  /** Unique within a config; the row's test id is `nav-<id>`. */
  id: string;
  /** Whether the item shows for this context; omitted, always. */
  available?: (ctx: NavCtx) => boolean;
  /** The row's test id, when it is not `nav-<id>` (ids tests and tours already name). */
  testId?: string;
  /**
   * The item's tour anchor (`data-tour`, Onboarding/JOURNEYS.md "Anchors"):
   * on a group, its whole block (the row and, open, its rows). Set only on
   * the items a journey points at; tourAnchors.test pins each one.
   */
  tour?: string;
}

export interface NavRow extends NavItemBase {
  kind: "row";
  label: string;
  icon?: ReactNode;
  /**
   * The icon a flat nav (a phone's drawer: NavRenderer `flat`) draws when
   * there is no `icon`: the nested nav draws this row bare (a drill-in's
   * views, an accordion's rows), but a flat nav draws every row like a
   * top-level one, icon included. navConfig.test holds every row to one.
   */
  flatIcon?: ReactNode;
  href: NavHref;
  match?: NavMatch;
  /**
   * The kind of console page the row opens, when it always opens one kind
   * (Services, Usage, Site insights…). It is what a host that does not serve
   * a page prunes by (navConfig/prune.ts `withoutPages`); rows that open no
   * console page, or several kinds, leave it unset.
   */
  page?: ConsolePage["kind"];
  /** Leaves the current area for another place (AI providers ↗ in Settings). */
  external?: boolean;
  badge?: NavBadgeOf;
  trail?: NavSlotId;
  /** A control after the row (Settings: the update button). */
  action?: NavSlotId;
}

export interface NavGroup extends NavItemBase {
  kind: "group";
  label: string;
  icon?: ReactNode;
  /**
   * The icon a flat nav (a phone's drawer: NavRenderer `flat`) draws when
   * there is no `icon`: the nested nav draws this row bare (a drill-in's
   * views, an accordion's rows), but a flat nav draws every row like a
   * top-level one, icon included. navConfig.test holds every row to one.
   */
  flatIcon?: ReactNode;
  children: NavChildren;
  /** Routes the group holds beyond its rows' own (a slot's: the chat pages for Agents ▾). */
  match?: NavMatch;
  trail?: NavSlotId;
  /** A count or a dot after the group's label, open or closed. */
  badge?: NavBadgeOf;
}

export interface NavArea extends NavItemBase {
  kind: "area";
  label: string;
  icon?: ReactNode;
  /**
   * The icon a flat nav (a phone's drawer: NavRenderer `flat`) draws when
   * there is no `icon`: the nested nav draws this row bare (a drill-in's
   * views, an accordion's rows), but a flat nav draws every row like a
   * top-level one, icon included. navConfig.test holds every row to one.
   */
  flatIcon?: ReactNode;
  /** The routes that are this area: on one, its rows replace the nav. */
  match: NavMatch;
  /** Where its row goes; omitted, its first row's page. */
  href?: NavHref;
  children: NavChildren;
  trail?: NavSlotId;
  /** A count or a dot after the area's row (not drawn inside the area: its rows carry their own). */
  badge?: NavBadgeOf;
  /** A control after its row (the footer's Settings: the update button). */
  action?: NavSlotId;
}

export interface NavHeading extends NavItemBase {
  kind: "heading";
  label: string;
}

export interface NavSlot extends NavItemBase {
  kind: "slot";
  slot: NavSlotId;
}

export type NavItem = NavRow | NavGroup | NavArea | NavHeading | NavSlot;

export interface NavConfig {
  items: readonly NavItem[];
  footer: readonly NavItem[];
  /** Pages whose own sections take the nav column on their routes (Settings); reached by a plain link. */
  sectionPages?: readonly NavArea[];
}
