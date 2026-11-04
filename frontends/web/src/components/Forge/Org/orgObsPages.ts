// Copyright (c) 2025 Reliant Labs

/**
 * The Observability panel's views, as org pages — the org counterpart of
 * Console/paths.ts `pageOfObsView` / `obsViewOfPage`.
 *
 * The panel still speaks in views ("open Logs for this service"); this says
 * which org page hosts each.
 */

import { orgPath, type OrgPage } from "../Console/paths";
import type { ObsView } from "../Observability/obsSearch";

/** Where an Observability view lives in the org console. */
export function orgPageOfObsView(view: ObsView | undefined): OrgPage {
  switch (view) {
    case undefined:
    case "overview":
      return { kind: "observability" };
    default:
      return { kind: "obs-view", view };
  }
}

/**
 * The panel view an org page hosts, or null when the page is not the panel's:
 * the two Observability pages with a body of their own (Metrics has no view
 * yet; Dashboards are a list of dashboards, not telemetry).
 */
export function obsViewOfOrgPage(page: OrgPage): ObsView | null {
  switch (page.kind) {
    case "observability":
      return "overview";
    case "obs-view":
      return page.view === "metrics" || page.view === "dashboards" ? null : page.view;
  }
}

/** Whether two org pages are the same page (the panel navigates in place within one). */
export function sameOrgPage(a: OrgPage, b: OrgPage): boolean {
  return orgPath(a) === orgPath(b);
}
