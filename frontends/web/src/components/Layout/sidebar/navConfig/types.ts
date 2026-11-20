// Copyright (c) 2026 Reliant Labs

/**
 * THE NAV AS DATA (U6c, prototype). Each nav variant (a.ts, b.ts, c.ts) is a
 * plain list of these items over the same pages, so the variant the owner
 * picks folds into the shell as config and the others delete as config.
 *
 *   row      a link: its href is built from the context, `match` says whether
 *            it is the page on screen
 *   group    an accordion (Workflows ▾): its rows open in place; clicking an
 *            open one closes it, and the group holding the page on screen
 *            opens on every navigation
 *   area     a drill-in (Agents ›, Observability ›, Settings ›): on any route
 *            the area's `match` claims, the area's rows REPLACE the nav under a
 *            "‹ Area" back header. Its row links to its first page, so being
 *            "in" an area is always a URL (Back, refresh and share restore it)
 *   heading  a quiet label over the rows after it
 *   slot     a live widget the shell renders (the chat list, New chat, the
 *            Inbox bell): data says where it goes, the shell says what it is
 *
 * A config's `footer` holds the rows under the nav's hairline (Inbox, Usage &
 * billing, Settings ›); an area there drills in like one above it.
 */

import type { ReactNode } from "react";

import type { ConsolePage, OrgPage } from "../../../Forge/Console/paths";
import type { GroupedNavInput } from "../../../Forge/Console/groupedNav";
import type { ShellMode } from "../shellProject";

export type NavVariant = "a" | "b" | "c";

/** Everything an item's href, match or availability reads: the route and the shell's scope. */
export interface NavCtx {
  variant: NavVariant;
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
  /** The Inbox, as a bell with its count. */
  | "inbox"
  /** A row's trail: the live automations pill (Workflows). */
  | "workflows-pill"
  /** A row's trail: a dot while a chat waits on the person (Agents). */
  | "chats-need-you"
  /** A row's control: the desktop app's update button (Settings). */
  | "update-button";

/** A count or a dot after a row's label. */
export type NavBadge = { type: "count"; value: number; tone?: "danger"; label?: string } | { type: "dot"; label: string };

interface NavItemBase {
  /** Unique within a config; the row's test id is `nav-<id>`. */
  id: string;
  /** Whether the item shows for this context; omitted, always. */
  available?: (ctx: NavCtx) => boolean;
  /** The row's test id, when it is not `nav-<id>` (ids tests and tours already name). */
  testId?: string;
}

export interface NavRow extends NavItemBase {
  kind: "row";
  label: string;
  icon?: ReactNode;
  href: NavHref;
  match?: NavMatch;
  /** Leaves the current area for another place (AI providers ↗ in Settings). */
  external?: boolean;
  badge?: (ctx: NavCtx) => NavBadge | undefined;
  trail?: NavSlotId;
}

export interface NavGroup extends NavItemBase {
  kind: "group";
  label: string;
  icon?: ReactNode;
  children: NavChildren;
  /** Routes the group holds beyond its rows' own (a slot's: the chat pages for Agents ▾). */
  match?: NavMatch;
  trail?: NavSlotId;
}

export interface NavArea extends NavItemBase {
  kind: "area";
  label: string;
  icon?: ReactNode;
  /** The routes that are this area: on one, its rows replace the nav. */
  match: NavMatch;
  /** Where its row goes; omitted, its first row's page. */
  href?: NavHref;
  children: NavChildren;
  trail?: NavSlotId;
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
  variant: NavVariant;
  /** One line for the variant picker. */
  title: string;
  items: readonly NavItem[];
  footer: readonly NavItem[];
}
