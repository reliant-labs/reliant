// Copyright (c) 2025 Reliant Labs

/**
 * A checkout's nested repositories, each with its own clone state: "Cloning
 * forge/…" while it lands, and on failure the repository and git's own
 * words, never a generic "failed". A set-up clones the root, then each
 * nested repository with a remote into its folder; the checkout stays
 * installing until every one settled.
 *
 * Renders nothing for a checkout with no nested repositories (or a server
 * older than nested clones, which reports none).
 */

import { AlertCircle, Check, Loader2 } from "lucide-react";

import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { nestedRepoLabel, type NestedRepoInstall } from "@/lib/projectPlacement";
import { cn } from "@/lib/utils";

export function NestedRepoStatus({
  repos,
  className,
}: {
  repos: ReadonlyArray<NestedRepoInstall> | undefined;
  className?: string;
}) {
  if (!repos || repos.length === 0) return null;
  return (
    <ul className={cn("space-y-0.5 text-xs", className)} aria-label="Nested repositories" data-testid="nested-repo-status">
      {repos.map((repo) => {
        const label = nestedRepoLabel(repo);
        const key = repo.relative_path || repo.name;
        switch (repo.install_state) {
          case ProjectInstallState.INSTALLING:
            return (
              <li key={key} className="flex items-center gap-1.5 text-muted-foreground">
                <Loader2 className="h-3 w-3 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                Cloning {label}…
              </li>
            );
          case ProjectInstallState.INSTALLED:
            return (
              <li key={key} className="flex items-center gap-1.5 text-muted-foreground">
                <Check className="h-3 w-3 shrink-0 text-success-ink" aria-hidden="true" />
                {label} cloned
              </li>
            );
          case ProjectInstallState.FAILED:
            return (
              <li key={key} className="flex items-start gap-1.5" role="alert">
                <AlertCircle className="mt-0.5 h-3 w-3 shrink-0 text-destructive-ink" aria-hidden="true" />
                <span className="min-w-0 break-words">
                  <span className="font-medium text-foreground">{label}</span>
                  <span className="text-destructive-ink">
                    : {repo.install_error || "its clone failed, and the machine gave no reason"}
                  </span>
                </span>
              </li>
            );
          default:
            return (
              <li key={key} className="text-muted-foreground">
                {label}: waiting to clone
              </li>
            );
        }
      })}
    </ul>
  );
}
