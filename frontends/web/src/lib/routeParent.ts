/**
 * Logical-parent navigation helper.
 *
 * In-UI close/back/X affordances should navigate to the logical parent route
 * (per the app's route hierarchy), not browser history and not always "/".
 * The browser/window back button stays history-based — only in-UI buttons use
 * this helper.
 *
 * Hierarchy:
 *   /workflow/$workflowName           → /workflows/library/$workflowName
 *                                       (the builder steps back to the workflow's page)
 *   /workflow/new                     → /workflows/library
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
 * list exits to the app. The builder steps back to the workflow's page, where
 * it is usually opened from (Edit); a new, unsaved one to the list. An entry
 * from elsewhere (a chat) records where it came from, and the builder's Back
 * returns there instead (getBuilderCloseNavigateOptions).
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
import type { HistoryState } from "@tanstack/react-router";
import { isSafeReturnTo } from "./returnTo";
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
    const name = pathname.slice("/workflow/".length).split("/")[0] ?? "";
    if (!name || name === "new") return { to: WORKFLOWS_LIBRARY_PATH };
    return { to: `${WORKFLOWS_LIBRARY_PATH}/${name}` };
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

/**
 * Where the builder was opened from, carried in history state.
 *
 * The workflow's page is only one way into the builder. A chat's workflow
 * viewer opens it too (its Edit pencil), and stepping "back" from there to the
 * workflow's page dropped the user somewhere they had never been. So an entry
 * that is not the workflow's own page records its location here, and the
 * builder's Back returns to it. History state, not the URL: it is the tab's own
 * navigation record, so a pasted builder link carries no stale origin, while a
 * reload (or the builder's own replace-navigations, which keep it) still does.
 */
export interface BuilderReturnState {
  builderReturnTo?: string;
}

/** The history state an entry into the builder passes to record `href` as its origin. */
export function builderReturnState(href: string): HistoryState & BuilderReturnState {
  return { builderReturnTo: href };
}

/**
 * The builder's Back: the in-app location it was opened from when an entry
 * recorded one (builderReturnState), else its logical parent. Anything that is
 * not a same-origin app path — a protocol-relative URL, an absolute URL, the
 * builder itself — is ignored rather than followed.
 */
export function getBuilderCloseNavigateOptions(
  pathname: string,
  state: unknown,
): { href: string } | RouteParentNavigateOptions {
  const returnTo = (state as BuilderReturnState | null | undefined)?.builderReturnTo;
  if (typeof returnTo === "string" && isSafeReturnTo(returnTo) && !isWorkflowBuilderPath(returnTo)) {
    return { href: returnTo };
  }
  return getParentRouteNavigateOptions(pathname);
}
