// Copyright (c) 2025 Reliant Labs

/**
 * The project switcher, in the sidebar's brand row: the one scope that
 * changes the nav (PROPOSAL §3.1). It names the FORGE project the console is
 * reporting on — the URL's subject — and lists the Reliant projects to switch
 * to.
 *
 * Choosing one keeps the reader on the same page of the other project
 * (ConsoleLayout.switchTarget). It does not select the project in the store
 * itself: the console layout selects whichever project declares the forge
 * project it lands on, so the store follows the URL rather than the other
 * way round.
 */

import { useEffect, useRef, useState } from "react";
import { Check, ChevronsUpDown } from "lucide-react";

import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { cn } from "@/lib/utils";
import { useProjectStore, type Project } from "@/store/projectStore";
import { useProjects } from "@/hooks/project-queries";

/** The project's monogram — the brand row on the collapsed rail. */
export function ProjectMonogram({ name }: { name: string }) {
  return (
    <span
      aria-hidden="true"
      className="grid h-5.5 w-5.5 shrink-0 place-items-center rounded-md bg-accent text-xs font-semibold text-on-accent"
    >
      {(name.charAt(0) || "?").toUpperCase()}
    </span>
  );
}

export interface ConsoleProjectSwitcherProps {
  forgeProject: string;
  onSelect: (project: Project) => void;
}

export function ConsoleProjectSwitcher({ forgeProject, onSelect }: ConsoleProjectSwitcherProps) {
  const { projects } = useProjects();
  const currentProject = useProjectStore((state) => state.currentProject);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);
  const escapeLayer = useEscapeLayer(open, () => setOpen(false));
  const { noDragRegionStyle } = useTitleBarChrome({ alignedToWindowEdge: false });

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [open]);

  return (
    <div ref={ref} className="relative min-w-0 flex-1" style={noDragRegionStyle}>
      <button
        type="button"
        data-testid="forge-project-switcher"
        onClick={() => setOpen((prev) => !prev)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Project: ${forgeProject}. Switch project`}
        className="flex h-8 w-full min-w-0 items-center gap-2 rounded-md px-1.5 text-left text-sm font-semibold text-ink transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        <ProjectMonogram name={forgeProject} />
        <span className="min-w-0 flex-1 truncate">{forgeProject}</span>
        <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />
      </button>

      {open && (
        <div
          role="menu"
          {...escapeLayer}
          data-testid="forge-project-menu"
          className="absolute left-0 top-10 z-[110] max-h-80 w-64 overflow-y-auto rounded-lg border border-border bg-surface p-1 shadow-md"
        >
          {projects.length === 0 ? (
            <p className="px-3 py-2 text-sm text-ink-muted">No projects yet.</p>
          ) : (
            projects.map((project) => {
              const name = (project.forge_project_name ?? "").trim();
              const active = name === forgeProject && (!currentProject || project.id === currentProject.id);
              return (
                <button
                  key={project.id}
                  type="button"
                  role="menuitem"
                  data-testid={`forge-project-menu-item-${project.id}`}
                  onClick={() => {
                    setOpen(false);
                    onSelect(project);
                  }}
                  className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm text-ink transition-colors hover:bg-ink/5"
                >
                  <Check className={cn("h-3.5 w-3.5 shrink-0", !active && "invisible")} aria-hidden="true" />
                  <span className="min-w-0 flex-1 truncate">{project.name}</span>
                  {name && name !== project.name && <span className="truncate text-xs text-ink-muted">{name}</span>}
                </button>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}
