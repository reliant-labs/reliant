// Copyright (c) 2025 Reliant Labs

import { cn } from "../../lib/utils";

/**
 * A project's initial in a small tile: the mark the sidebar puts on a project
 * section's header, so a project reads as the level above its workspaces and
 * chats rather than as one more label beside them. Decorative — the project's
 * name is always rendered beside it.
 *
 * A badge glyph (16px, the letter at the badge size) in an ink wash. It does not
 * turn blue for the project holding the open chat: the open chat's row says
 * that, and a selection is not a link.
 */
export function ProjectMark({ name, className }: { name: string; className?: string }) {
  const initial = Array.from(name.trim())[0]?.toUpperCase() ?? "?";
  return (
    <span
      aria-hidden="true"
      data-testid="project-mark"
      className={cn(
        "flex size-4 shrink-0 items-center justify-center rounded-md bg-foreground/[0.08] text-2xs font-semibold leading-none text-foreground",
        className,
      )}
    >
      {initial}
    </span>
  );
}
