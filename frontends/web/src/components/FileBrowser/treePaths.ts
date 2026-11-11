/**
 * Paths in the file tree's own namespace.
 *
 * Every node the tree lists has a path relative to the root of the checkout it
 * shows ("src/a.ts"). A viewer can hold a file the tree does not list — an
 * absolute path an agent opened ("/home/workspace/projects/x/README.md") or a
 * literal "~/..." path from a chat — and revealing it used to expand every
 * prefix of that path: "/home", "/home/workspace", "~", "~/projects", ...
 *
 * Those entries never match a node, so nothing showed, but the tree's refresh
 * re-read every expanded directory on each pushed file-change event. The server
 * refuses the absolute ones ("absolute path is outside the workspace") and the
 * daemon cannot find the "~" ones, so one open file kept a tab sending ~50
 * failing GetFileTree calls a minute for as long as it stayed open.
 */

/**
 * The directories to expand so `path` shows in the tree, outermost first.
 * Empty for a path outside the tree's namespace (absolute, or "~"-relative):
 * no node can have it, so expanding toward it only produces refused reads.
 */
export function treeAncestorDirs(path: string): string[] {
  if (!isTreePath(path)) return [];
  const dirs: string[] = [];
  let slash = path.indexOf("/");
  while (slash > 0) {
    dirs.push(path.substring(0, slash));
    slash = path.indexOf("/", slash + 1);
  }
  return dirs;
}

/** Whether `path` can name a node of the tree (a checkout-relative path). */
export function isTreePath(path: string): boolean {
  return path !== "" && !path.startsWith("/") && path !== "~" && !path.startsWith("~/");
}

/**
 * `expanded` plus the ancestors of `path`, or `expanded` itself (same
 * reference) when nothing needs adding, so a state setter can skip a render.
 */
export function withAncestorsExpanded(expanded: Set<string>, path: string): Set<string> {
  const missing = treeAncestorDirs(path).filter((dir) => !expanded.has(dir));
  if (missing.length === 0) return expanded;
  const next = new Set(expanded);
  for (const dir of missing) next.add(dir);
  return next;
}
