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
 *   anything else           → /
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
  return { to: "/", search: {} };
}
