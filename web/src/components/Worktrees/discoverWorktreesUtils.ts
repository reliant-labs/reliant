import type { DiscoveredWorktree } from "../../store/worktreeStore";

export type DiscoverStatusFilter = "unimported" | "imported" | "all";
export type DiscoverSortField = "name" | "branch" | "path";
export type DiscoverSortDirection = "asc" | "desc";

export interface DiscoverWorktreeQuery {
  search: string;
  statusFilter: DiscoverStatusFilter;
  sortField: DiscoverSortField;
  sortDirection: DiscoverSortDirection;
}

export function deriveVisibleWorktrees(
  worktrees: DiscoveredWorktree[],
  query: DiscoverWorktreeQuery
): DiscoveredWorktree[] {
  const normalizedSearch = query.search.trim().toLowerCase();

  let result = [...worktrees];

  if (query.statusFilter === "unimported") {
    result = result.filter((w) => !w.is_imported);
  } else if (query.statusFilter === "imported") {
    result = result.filter((w) => w.is_imported);
  }

  if (normalizedSearch) {
    result = result.filter((w) => {
      const name = w.name.toLowerCase();
      const branch = w.branch.toLowerCase();
      const path = w.path.toLowerCase();
      return (
        name.includes(normalizedSearch) ||
        branch.includes(normalizedSearch) ||
        path.includes(normalizedSearch)
      );
    });
  }

  const direction = query.sortDirection === "asc" ? 1 : -1;
  result.sort((a, b) => {
    const left = (a[query.sortField] || "").toLowerCase();
    const right = (b[query.sortField] || "").toLowerCase();
    return left.localeCompare(right) * direction;
  });

  return result;
}
