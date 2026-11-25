// Copyright (c) 2026 Reliant Labs

/**
 * What the shell's nav knows about the open app's console: the shape
 * useConsoleNavModel.ts fills from the app's roster, alerts and dashboards,
 * and the shell's nav items read as `ctx.console`
 * (Layout/sidebar/navConfig/types.ts). Types only: the rows themselves are
 * the nav config's (navConfig/items.tsx).
 */

import type { DashboardPageKind } from "../Observability/Dashboards/dashboardModel";
import type { ConsolePage } from "./paths";

export interface ConsoleNavEnv {
  name: string;
  preview: boolean;
  /** What this environment's QUEUED deploy waits on ("billing"), when one is. */
  queuedOn?: string;
}

export interface ConsoleNavDashboard {
  title: string;
  dashKind: DashboardPageKind;
  key: string;
}

export interface ConsoleNavInput {
  project: string;
  page: ConsolePage;
  /** The scope every link carries. */
  env: string;
  envs: ConsoleNavEnv[];
  /** Alerts firing in scope; a danger count on Alerts. */
  alertsFiring?: number;
  /** The caller's starred dashboards. */
  starred?: ConsoleNavDashboard[];
  /** The open dashboard's title, for the breadcrumb (its key until the index answers). */
  dashboardTitle?: string;
  /** Omitted/unknown keeps useful views visible; false means a definite empty read. Sessions is always listed. */
  optionalViews?: { profiles: boolean };
  /**
   * Live counts other parts of the console hand the nav for its badges,
   * keyed by the source's id (e.g. `tests`). Each is read from data a page
   * already holds (a React Query cache entry its page reads anyway), so a
   * badge never costs a request of its own. Filled in useConsoleNavModel;
   * read by a nav item's `badge` (navConfig/types.ts) as
   * `ctx.console?.navCounts?.<id>`. Absent or 0: no badge.
   */
  navCounts?: Readonly<Partial<Record<string, number>>>;
}

/** The Product analytics hub: the dashboard it opens on, and every dashboard key that is one of its tabs. */
export interface ProductAnalyticsHub {
  open: ConsolePage;
  keys: readonly string[];
}

export interface GroupedNavInput extends ConsoleNavInput {
  /** The project's Product analytics hub, when it has one: its dashboards light their Analytics tab. */
  productAnalytics?: ProductAnalyticsHub;
  /** The app's own dashboards (its repo's, the org's saved ones): listed under Dashboards. */
  customDashboards?: readonly ConsoleNavDashboard[];
}
