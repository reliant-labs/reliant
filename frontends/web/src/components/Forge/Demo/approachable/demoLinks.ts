// Copyright (c) 2026 Reliant Labs

/**
 * Where the plain-language pages send a reader in the sample app: one place
 * to spell each, built from the console's own paths (Console/paths.ts).
 */

import { consolePath, withSearch } from "../../Console/paths";
import { FIXTURE_PROJECT } from "../../Observability/__fixtures__/fixtureApi";
import { DEMO_INCIDENT } from "../demoData/shopStory";

const at = (page: Parameters<typeof consolePath>[1], search?: Record<string, unknown>) => withSearch(consolePath(FIXTURE_PROJECT, page), search);

/** The orders service's generated dashboard (Dashboards → Services). */
export const SERVICE_DASHBOARD_KEY = "service-orders";

/** One trace, one recording, one problem, one alert rule: the console's own detail pages. */
export const traceHref = (traceId: string) => at({ kind: "obs-view", view: "traces" }, { trace: traceId });
export const sessionHref = (sessionId: string) => at({ kind: "obs-view", view: "sessions" }, { session: sessionId });
export const issueHref = (issueId: string) => at({ kind: "obs-view", view: "errors" }, { issue: issueId });
export const ruleHref = (rule: string) => at({ kind: "obs-view", view: "alerts" }, { rule });

/** The Product analytics hub's first tab (Users · Product · Acquisition). */
const PEOPLE_DASHBOARD = { kind: "dashboard", dashKind: "release", key: "users" } as const;

export const DEMO_LINKS = {
  happening: at({ kind: "overview" }),
  errors: at({ kind: "obs-view", view: "errors" }),
  incidentIssue: at({ kind: "obs-view", view: "errors" }, { issue: DEMO_INCIDENT.issueId }),
  traces: at({ kind: "obs-view", view: "traces" }),
  sessions: at({ kind: "obs-view", view: "sessions" }),
  alerts: at({ kind: "obs-view", view: "alerts" }),
  serviceDashboard: at({ kind: "dashboard", dashKind: "release", key: SERVICE_DASHBOARD_KEY }),
  releases: at({ kind: "releases" }),
  badRelease: at({ kind: "release", version: DEMO_INCIDENT.release }),
  releasesDashboard: at({ kind: "dashboard", dashKind: "release", key: "releases" }),
  people: at(PEOPLE_DASHBOARD),
  peopleAcquisition: at({ kind: "dashboard", dashKind: "release", key: "acquisition" }),
} as const;

/** The dashboards that are "People" in the simple view: the Product analytics hub's tabs. */
export const PEOPLE_DASHBOARD_KEYS: readonly string[] = ["users", "product", "acquisition"];
