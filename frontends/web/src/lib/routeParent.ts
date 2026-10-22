/**
 * Logical-parent navigation helper.
 *
 * In-UI close/back/X affordances should navigate to the logical parent route
 * (per the app's route hierarchy), not browser history and not always "/".
 * The browser/window back button stays history-based — only in-UI buttons use
 * this helper.
 *
 * Hierarchy:
 *   /workflow/$workflowName           → /workflows/library (the builder)
 *   /workflows/<tab>/<id>             → /workflows/<tab>   (a detail page)
 *   /workflows, /workflows/<tab>      → /
 *   /settings, /settings/*            → /
 *   /forge, /forge/*                  → /
 *   anything else                     → /
 *
 * Forge's in-UI close is an EXIT from the whole surface, not a step back
 * within it: the header's close sits on every forge page, and a reader who
 * presses it on /forge/env/prod means "leave forge", not "go to the
 * Overview" — the Overview is one click away in the sidebar and in the
 * page's own "All environments" link. So every forge path exits to /.
 * These cases are written out rather than left to the fallback because the
 * fallback's answer being correct here is a coincidence worth pinning.
 *
 * The Workflows area (lib/workflowsArea.ts) is the opposite of forge: each
 * tab's list is a real hub with its own actions, so a detail page — a
 * workflow, a run, an automation — steps back into its tab's list, and the
 * list exits to the app. The builder steps back to the Library, where the
 * workflow it was opened from lives.
 *
 * Settings is deliberately flat. Unlike the Workflows area, /settings and
 * /settings/$section render the same SettingsPage — the bare path just falls
 * back to the default section. Treating /settings as a
 * parent therefore made closing a section land back on the account tab and
 * require a second close, so every settings path exits straight to /.
 *
 * The function is pure and takes a pathname string so it can be unit tested
 * without a router. Callers spread the result into `useNavigate()({...})`.
 */
import {
  WORKFLOWS_LIBRARY_PATH,
  WORKFLOWS_TABS,
  isWorkflowBuilderPath,
  isWorkflowsDetailPath,
  workflowsTabForPath,
} from "./workflowsArea";

export type RouteParentNavigateOptions = {
  to: string;
  search?: Record<string, never>;
};

export function getParentRouteNavigateOptions(
  pathname: string,
): RouteParentNavigateOptions {
  if (isWorkflowBuilderPath(pathname)) {
    return { to: WORKFLOWS_LIBRARY_PATH };
  }
  if (pathname === "/forge" || pathname.startsWith("/forge/")) {
    return { to: "/", search: {} };
  }
  if (isWorkflowsDetailPath(pathname)) {
    const tab = WORKFLOWS_TABS.find((candidate) => candidate.key === workflowsTabForPath(pathname));
    if (tab) return { to: tab.path };
  }
  return { to: "/", search: {} };
}
