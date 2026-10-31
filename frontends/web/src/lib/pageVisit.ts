/**
 * `page_visited` payload helpers. The page name is the matched route's
 * TEMPLATE (`/project/$projectId`), never the URL: ids, query strings and
 * hashes (a claim token rides the hash) must not reach analytics.
 */

/** The leaf match's route template, without a trailing slash (root stays `/`). */
export function routeTemplate(fullPath: string | undefined): string {
  if (!fullPath) return "/";
  const trimmed = fullPath.length > 1 ? fullPath.replace(/\/+$/, "") : fullPath;
  return trimmed || "/";
}
