// Copyright (c) 2025 Reliant Labs

import { cn } from "../../lib/utils";

/**
 * A project's initial in a small tile: the mark the sidebar puts on a project
 * section's header, so a project reads as the level above its workspaces and
 * chats rather than as one more label beside them. Decorative — the project's
 * name is always rendered beside it.
 *
 * An inset (bg-background + border) on the sidebar's bg-card, per the
 * web-styling elevation rule; the project holding the open chat takes the
 * primary tint, as its name does.
 */
export function ProjectMark({ name, active = false, className }: { name: string; active?: boolean; className?: string }) {
  const initial = Array.from(name.trim())[0]?.toUpperCase() ?? "?";
  return (
    <span
      aria-hidden="true"
      data-testid="project-mark"
      className={cn(
        "flex h-5 w-5 shrink-0 items-center justify-center rounded border text-2xs font-semibold leading-none",
        active ? "border-primary/40 bg-primary/10 text-primary" : "border-border/60 bg-background text-muted-foreground",
        className,
      )}
    >
      {initial}
    </span>
  );
}
