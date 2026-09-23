// Copyright (c) 2025 Reliant Labs

/**
 * The forge surface's top bar — the content of SidebarLayout's header slot.
 *
 * It carries the two things that are properties of the WHOLE surface rather
 * than of any one screen: which project everything below is reporting on, and
 * the way out.
 *
 * WHY THE PROJECT SWITCHER IS HERE AND NOT IN THE SIDEBAR. The sidebar lists
 * the places this surface has; the project is the SCOPE those places are read
 * in. Putting the scope above the content it scopes, on the same band as the
 * exit, keeps the sidebar a pure list of destinations — a switcher wedged into
 * it would read as a fifth destination. It is also where Vercel puts the
 * equivalent control, which is the reference the user chose.
 *
 * WHY THE EXIT IS ON THE RIGHT, unlike SettingsHeader's left-hand one. The
 * window's top-left corner now belongs to the SIDEBAR's brand row, which is
 * also where macOS puts the traffic lights — so the leading edge of this bar
 * is no longer the leading edge of the window, and an exit placed there would
 * sit in the middle of the chrome rather than at the start of it. Right-aligned
 * it reads as a dismiss for the surface, which is what it is.
 *
 * Close SEMANTICS are unchanged: X + "Close" in Electron (matching the
 * window-control aesthetic), a back arrow + "Back" on the web (a browser tab
 * has no window-close semantics). The handler navigates to the logical parent
 * route rather than calling history.back() — see lib/routeParent.ts for why.
 */

import { useEffect, useRef, useState } from "react";
import { ArrowLeft, Check, ChevronDown, X } from "lucide-react";

import { useProjectStore, type Project } from "@/store/projectStore";
import { cn } from "@/lib/utils";
import { Tooltip } from "../ui/Tooltip";
import { useTitleBarChrome } from "../../hooks/useTitleBarChrome";

export interface ForgeHeaderProps {
  onClose: () => void;
  /** Called after a project is picked, so the layout can sync the URL param. */
  onProjectSelected?: (project: Project) => void;
}

export function ForgeHeader({ onClose, onProjectSelected }: ForgeHeaderProps) {
  // `alignedToWindowEdge: false` because the sidebar, not this bar, now spans
  // the window's leading edge — the traffic lights are cleared over there.
  const { isElectron, dragRegionStyle, noDragRegionStyle } = useTitleBarChrome({
    alignedToWindowEdge: false,
  });

  const currentProject = useProjectStore((state) => state.currentProject);
  const projects = useProjectStore((state) => state.projects);
  const selectProject = useProjectStore((state) => state.selectProject);

  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement | null>(null);

  // Dismiss on an outside click. Escape is NOT handled here: ForgeLayout binds
  // Escape to close the whole surface, and a menu that swallowed it would make
  // the key mean two different things depending on invisible state.
  useEffect(() => {
    if (!menuOpen) return;
    const onPointerDown = (event: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) {
        setMenuOpen(false);
      }
    };
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [menuOpen]);

  return (
    <div
      className={cn("flex w-full items-center gap-3", isElectron && "cursor-move")}
      style={dragRegionStyle}
    >
      {/* The scope everything below is read in. */}
      <div
        ref={menuRef}
        className="relative flex cursor-default items-center"
        style={noDragRegionStyle}
      >
        <button
          type="button"
          data-testid="forge-project-switcher"
          onClick={() => setMenuOpen((open) => !open)}
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          className="inline-flex h-8 max-w-[18rem] items-center gap-2 rounded-md border border-border px-2.5 text-sm text-foreground transition-colors hover:border-border-strong"
        >
          {/* A project name is a user-chosen label, not an identifier, so it is
              not mono. */}
          <span className="truncate">{currentProject?.name ?? "Choose a project"}</span>
          <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        </button>

        {menuOpen && (
          <div
            role="menu"
            data-testid="forge-project-menu"
            className="absolute left-0 top-10 z-[110] max-h-80 w-72 overflow-y-auto rounded-lg border border-border bg-card p-1 shadow-md"
          >
            {projects.length === 0 ? (
              <p className="px-3 py-2 text-sm text-muted-foreground">No projects yet.</p>
            ) : (
              projects.map((project) => {
                const active = project.id === currentProject?.id;
                return (
                  <button
                    key={project.id}
                    type="button"
                    role="menuitem"
                    data-testid={`forge-project-menu-item-${project.id}`}
                    onClick={() => {
                      setMenuOpen(false);
                      void (async () => {
                        await selectProject(project);
                        onProjectSelected?.(project);
                      })();
                    }}
                    className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm text-foreground transition-colors hover:bg-muted/70"
                  >
                    <Check className={cn("h-3.5 w-3.5 shrink-0", !active && "invisible")} />
                    <span className="truncate">{project.name}</span>
                  </button>
                );
              })
            )}
          </div>
        )}
      </div>

      {/* Draggable filler */}
      <div className="flex-1" style={dragRegionStyle} />

      <div className="flex cursor-default items-center" style={noDragRegionStyle}>
        <Tooltip
          content={isElectron ? "Close forge (Esc)" : "Back to app (Esc)"}
          placement="bottom"
          delay={300}
        >
          <button
            onClick={onClose}
            className="inline-flex h-8 items-center gap-1.5 rounded-md px-2.5 text-sm text-muted-foreground transition-colors hover:bg-muted/70 hover:text-foreground"
            aria-label={isElectron ? "Close forge" : "Back to app"}
            data-testid="forge-close"
          >
            {isElectron ? <X className="h-4 w-4" /> : <ArrowLeft className="h-4 w-4" />}
            <span>{isElectron ? "Close" : "Back"}</span>
          </button>
        </Tooltip>
      </div>
    </div>
  );
}
