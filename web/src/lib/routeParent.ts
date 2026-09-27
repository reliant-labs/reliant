/**
 * Logical-parent navigation helper.
 *
 * In-UI close/back/X affordances should navigate to the logical parent route
 * (per the app's route hierarchy), not browser history and not always "/".
 * The browser/window back button stays history-based — only in-UI buttons use
 * this helper.
 *
 * Hierarchy:
 *   /workflow/$workflowName → /workflow
 *   /workflow               → /
 *   /settings, /settings/*  → /
 *   /forge, /forge/*        → /
 *   anything else           → /
 *
 * Forge's in-UI close is an EXIT from the whole surface, not a step back
 * within it: the header's close sits on every forge page, and a reader who
 * presses it on /forge/env/prod means "leave forge", not "go to the
 * Overview" — the Overview is one click away in the sidebar and in the
 * page's own "All environments" link. So every forge path exits to /.
 * These cases are written out rather than left to the fallback because the
 * fallback's answer being correct here is a coincidence worth pinning.
 *
 * Settings is deliberately flat. Unlike /workflow, which is a distinct hub
 * view, /settings and /settings/$section render the same SettingsPage — the
 * bare path just falls back to the default section. Treating /settings as a
 * parent therefore made closing a section land back on the account tab and
 * require a second close, so every settings path exits straight to /.
 *
 * The function is pure and takes a pathname string so it can be unit tested
 * without a router. Callers spread the result into `useNavigate()({...})`.
 */
export type RouteParentNavigateOptions = {
  to: string;
  search?: Record<string, never>;
};

export function getParentRouteNavigateOptions(
  pathname: string,
): RouteParentNavigateOptions {
  if (pathname.startsWith("/workflow/")) {
    return { to: "/workflow" };
  }
  if (pathname === "/workflow") {
    return { to: "/", search: {} };
  }
  if (pathname === "/forge" || pathname.startsWith("/forge/")) {
    return { to: "/", search: {} };
  }
  return { to: "/", search: {} };
}
