// Copyright (c) 2026 Reliant Labs

/**
 * Which nav row is the page on screen. Every comparison is between org-less
 * paths, and the targets come from lib/paths' builders, so a page in either
 * form (`/inbox`, `/o/<org>/inbox`) lights the same row and no path is spelled
 * here (__tests__/navPaths.guard.test.ts).
 */

import { orgFreePath } from "@/lib/paths/org";

function pathOf(href: string): string {
  const end = href.search(/[?#]/);
  return orgFreePath(end === -1 ? href : href.slice(0, end));
}

/** `pathname` is the page `href` opens, or one below it (a detail page). */
export function isAtOrBelow(pathname: string, href: string): boolean {
  const here = orgFreePath(pathname);
  const target = pathOf(href);
  if (target === "/") return here === "/";
  return here === target || here.startsWith(`${target}/`);
}

/** `pathname` is exactly the page `href` opens. */
export function isAt(pathname: string, href: string): boolean {
  return orgFreePath(pathname) === pathOf(href);
}
