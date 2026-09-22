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
 * Forge is flat for the same reason settings is, but arrived at differently:
 * /forge/topology, /forge/status and /forge/secrets are peer TABS of one
 * surface, not a hub and its children, so there is no intermediate view to step
 * back to. Treating /forge as a parent would make closing a tab land on a bare
 * /forge, which only redirects to topology — a close that visibly does nothing.
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
