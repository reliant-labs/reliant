// Copyright (c) 2025 Reliant Labs

/**
 * The home view's one app prompt: on a project that is not a forge project,
 * the way to build an app.
 *
 * A forge project gets nothing here. What it has shipped, and where it is
 * live, is the sidebar's to show (SidebarApps); repeating it in the middle of
 * the new-chat screen only pushed the composer down.
 */

import { ArrowRight, Rocket } from "lucide-react";

import { cn } from "@/lib/utils";
import { useProjectStore } from "@/store/projectStore";

/** `onStart` opens the app-building composer; the user sends it. */
export function HomeBuildApp({ onStart }: { onStart: () => void }) {
  // is_forge (a forge.yaml at the repo root) is set when the project is
  // created; forge_project_name arrives once a daemon has read it. Either one
  // makes this a forge project.
  const isForgeProject = useProjectStore((state) => {
    const project = state.currentProject;
    return Boolean(project?.is_forge) || (project?.forge_project_name ?? "").trim() !== "";
  });
  if (isForgeProject) return null;

  return (
    <button
      type="button"
      onClick={onStart}
      className={cn(
        "group flex w-full items-center gap-3 rounded-lg border border-dashed border-border bg-card/60 px-4 py-3 text-left transition-colors",
        "hover:border-primary/50 hover:bg-card focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      )}
      data-testid="home-build-app"
    >
      <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary">
        <Rocket className="h-4 w-4" aria-hidden="true" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-medium text-foreground">Build an app</span>
        <span className="block text-xs text-muted-foreground">
          Describe it. Reliant builds it with forge and deploys it to a live URL.
        </span>
      </span>
      <ArrowRight className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
    </button>
  );
}
