// Copyright (c) 2025 Reliant Labs

/**
 * ProjectRowSummary — what one catalog row says about itself, wherever a row
 * is listed: the project selector, the Projects page, the project page's
 * header. Its name, its repos, whether a machine has the code, and its app
 * chips (AppChip). An app-only row ("apps without code here") says so in the
 * place a project's repos go.
 *
 * Layout only: the caller owns the row's control (an option, a link, a
 * button) and its selection state.
 */

import { AppWindow, FolderGit2, HardDrive, Pin, Users } from "lucide-react";

import { cn } from "@/lib/utils";
import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import type { CatalogRow, ProjectCatalogRow } from "@/services/projects/catalog";

import { AppChip } from "./AppChip";

export interface ProjectRowSummaryProps {
  row: CatalogRow;
  pinned?: boolean;
  /** Chips link into their app's console (default false: the row is usually the control). */
  interactiveChips?: boolean;
  /** How many chips before "+N" (default 3). */
  maxChips?: number;
  className?: string;
  /** On the chips' group: a page that lays them out itself at some widths hides them here (`max-sm:hidden`). */
  chipsClassName?: string;
}

/** The repos line: names (or the folder for a project with none listed). */
function reposText(row: ProjectCatalogRow): string {
  if (row.repos.length === 0) return row.project.path || "No repository";
  const names = row.repos.map((r) => r.remote || r.name);
  return names.length > 2 ? `${names.slice(0, 2).join(", ")} +${names.length - 2}` : names.join(", ");
}

/** Where the code is: the machines with an installed checkout, in words. */
export function checkoutText(row: ProjectCatalogRow): string {
  if (row.shared) return "Shared with you";
  const installed = row.checkouts.filter((c) => c.installState === ProjectInstallState.INSTALLED);
  if (installed.length === 0) {
    return row.checkouts.some((c) => c.installState === ProjectInstallState.INSTALLING)
      ? "Cloning…"
      : "Not on a machine";
  }
  const first = installed[0].daemonName || "a machine";
  return installed.length === 1 ? `On ${first}` : `On ${first} +${installed.length - 1}`;
}

export function ProjectRowSummary({ row, pinned = false, interactiveChips = false, maxChips = 3, className, chipsClassName }: ProjectRowSummaryProps) {
  const apps = row.kind === "project" ? row.apps : [row.app];
  const shown = apps.slice(0, maxChips);
  const more = apps.length - shown.length;
  const Icon = row.kind === "project" ? FolderGit2 : AppWindow;
  return (
    <div className={cn("flex min-w-0 flex-1 items-center gap-3", className)} data-testid="project-row-summary" data-row-key={row.key}>
      <Icon aria-hidden className="h-4 w-4 shrink-0 text-muted-foreground" />
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="truncate text-ui font-medium text-foreground">{row.name}</span>
          {pinned ? <Pin aria-label="Pinned" className="h-3 w-3 shrink-0 text-muted-foreground" /> : null}
        </span>
        <span className="flex min-w-0 items-center gap-1.5 text-meta text-muted-foreground">
          {row.kind === "project" && row.partial ? (
            <span className="truncate">{row.project.path}</span>
          ) : row.kind === "project" ? (
            <>
              <span className="truncate">{reposText(row)}</span>
              <span aria-hidden>·</span>
              <span className="flex shrink-0 items-center gap-1" data-testid="row-checkout" data-has-checkout={row.hasCheckout}>
                {row.shared ? <Users aria-hidden className="h-3 w-3" /> : <HardDrive aria-hidden className={cn("h-3 w-3", !row.hasCheckout && "opacity-50")} />}
                {checkoutText(row)}
              </span>
            </>
          ) : (
            <span className="truncate">App without code here</span>
          )}
        </span>
      </div>
      {shown.length > 0 ? (
        <span className={cn("flex min-w-0 shrink items-center justify-end gap-1", chipsClassName)}>
          {shown.map((app) => (
            <AppChip key={`${app.repoId ?? ""}/${app.subpath ?? ""}/${app.name}`} app={app} interactive={interactiveChips} />
          ))}
          {more > 0 ? <span className="shrink-0 text-meta text-muted-foreground">+{more}</span> : null}
        </span>
      ) : null}
    </div>
  );
}
