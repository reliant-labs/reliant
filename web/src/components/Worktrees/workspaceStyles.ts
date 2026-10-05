/**
 * Shared class strings for the workspace surfaces (Settings → Workspaces and
 * the Workspaces viewer tab). They follow the forge table vocabulary used by
 * components/Forge/Overview/EnvironmentTable.tsx, so a workspace list reads
 * like every other data table in the app.
 *
 * The table wrapper is an `@container`: columns hide by the width the table
 * actually has, not the viewport, because the same list renders full-width in
 * Settings and inside a 256px sidebar in the viewer tab.
 */

export const workspaceTable = {
  wrapper: "@container overflow-x-auto rounded-lg border border-border bg-card",
  table: "w-full border-collapse text-sm",
  headRow: "border-b border-border",
  headCell:
    "whitespace-nowrap px-3 py-2 text-left text-2xs font-medium uppercase tracking-wide text-muted-foreground",
  row: "border-b border-border/60 last:border-0",
  cell: "px-3 py-2.5 align-middle",
} as const;

/** Columns that only appear once the table is wide enough to hold them. */
export const workspaceColumn = {
  branch: "hidden @xl:table-cell",
  chat: "hidden @3xl:table-cell",
  time: "hidden @2xl:table-cell",
  cleanup: "hidden @lg:table-cell",
  /** The branch shown as a sub-line under the name while its column is hidden. */
  branchSubline: "@xl:hidden",
} as const;

/** An icon-only row action. Pair with a Tooltip and an aria-label. */
export const workspaceIconButton =
  "inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-40";

export const workspaceIconButtonDanger =
  "hover:bg-destructive/10 hover:text-destructive-ink";

/** Quiet context chip (project, machine) in a page header's meta row. */
export const workspaceChip =
  "inline-flex items-center gap-1.5 rounded-md border border-border bg-card px-2 py-0.5 text-xs text-muted-foreground";
