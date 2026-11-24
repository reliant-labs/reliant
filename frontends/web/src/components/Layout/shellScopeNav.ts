// Copyright (c) 2026 Reliant Labs

/**
 * A person's pick of the shell's scope, put in the URL (owner, 2026-11-24:
 * the URL is the only source of view state): `?projects=all`, `?focus=<key>`
 * (lib/urlState/shellScope.ts). The chat shell and the console carry both
 * to every page (routeSchemas PROJECTS_CARRIED), so a pick names them, or
 * names them as undefined to drop them; leaving one out would keep it.
 */

import { latestPathname } from "@/lib/chatRoute";
import { focusParam, projectsParam, type ShellScope } from "@/lib/urlState/shellScope";
import { writeParam, type SearchRecord } from "@/lib/urlState/searchParams";

type RouterLike = { navigate: (opts: { to: string; search?: unknown; replace?: boolean }) => unknown };

function router(): RouterLike | null {
  return (globalThis as { __RELIANT_ROUTER?: RouterLike }).__RELIANT_ROUTER ?? null;
}

/** The shell's params for `scope` (undefined: none, the page's own) and `focus`. */
export function shellScopeSearch(scope: ShellScope | undefined, focus?: string | null): SearchRecord {
  return writeParam(writeParam({}, projectsParam, scope), focusParam, scope && scope.mode !== "one" ? (focus ?? undefined) : undefined);
}

/** Go to `to` with the nav's sections on `focus`, the scope on screen kept (the carried `projects`). */
export function navigateWithFocus(to: string, focus: string): void {
  void router()?.navigate({ to, search: () => writeParam({}, focusParam, focus) });
}

/**
 * Show `scope`: on `to` (another page: only the shell's params ride along,
 * with what the root keeps, like `?tour=`), else on the page itself, its
 * other params kept. A push, so Back undoes the pick.
 */
export function navigateWithShellScope(scope: ShellScope | undefined, { to, focus }: { to?: string; focus?: string | null } = {}): void {
  const params = shellScopeSearch(scope, focus);
  void router()?.navigate(
    to ? { to, search: () => params } : { to: latestPathname(), search: (prev: SearchRecord) => ({ ...(prev ?? {}), ...params }) },
  );
}
