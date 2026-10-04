// Copyright (c) 2025 Reliant Labs

/**
 * The forge surface's SCOPE CONTROLS: which project everything is reporting
 * on, and the way out. Both live in the sidebar now — the project switcher
 * under the brand, the exit as a quiet icon in the brand row.
 *
 * WHY THERE IS NO TOP BAR ANY MORE. It used to be a full-width band carrying a
 * project button on the left and "Close" on the right, with nothing between
 * them. It named neither the page nor the environment, so every screen opened
 * with sixty pixels of chrome that said nothing and a second header under it
 * that said what the page was. Each page now owns its header (title, kind,
 * actions), and the scope sits with the navigation it scopes.
 *
 * The switcher reads as scope, not as a sixth destination: it is a select
 * control (label + chevron, bordered) above the nav, not a nav row.
 *
 * Selecting a project keeps the user IN Forge. projectStore.selectProject
 * leaves /forge alone (see syncProjectUrl), and ForgeLayout re-points the
 * `project` param — so switching re-reads the same screen for the new
 * project instead of dropping the user into its chat view.
 *
 * Close SEMANTICS are unchanged: X in Electron, a back arrow on the web, and
 * Esc everywhere (bound in ForgeLayout). It navigates to the logical parent
 * route rather than history.back() — see lib/routeParent.ts.
 */

import { useEffect, useRef, useState } from "react";
import { ArrowLeft, Check, ChevronsUpDown, X } from "lucide-react";

import { Tooltip } from "@/components/ui/Tooltip";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { cn } from "@/lib/utils";
import { useProjectStore, type Project } from "@/store/projectStore";

export interface ForgeProjectSwitcherProps {
  /** Called after the store selection settles, so the layout can sync the URL. */
  onProjectSelected?: (project: Project) => void;
}

export function ForgeProjectSwitcher({ onProjectSelected }: ForgeProjectSwitcherProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const projects = useProjectStore((state) => state.projects);
  const selectProject = useProjectStore((state) => state.selectProject);

  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement | null>(null);

  // Dismiss on an outside click (the DROPDOWN_STANDARDS pattern). Escape is NOT
  // handled here: ForgeLayout binds Escape to close the whole surface, and a
  // menu that swallowed it would make the key mean two things.
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
    <div ref={menuRef} className="relative">
      <button
        type="button"
        data-testid="forge-project-switcher"
        onClick={() => setMenuOpen((open) => !open)}
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        aria-label={`Project: ${currentProject?.name ?? "none"}. Switch project`}
        className="flex h-9 w-full items-center gap-2 rounded-md border border-border bg-background px-2.5 text-left text-sm text-foreground transition-colors hover:border-border-strong focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span className="flex min-w-0 flex-1 flex-col leading-tight">
          <span className="text-2xs uppercase tracking-wide text-muted-foreground">Project</span>
          {/* A user-chosen label, not an identifier, so not mono. */}
          <span className="truncate font-medium">{currentProject?.name ?? "Choose a project"}</span>
        </span>
        <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      </button>

      {menuOpen && (
        <div
          role="menu"
          data-testid="forge-project-menu"
          className="absolute left-0 right-0 top-11 z-[110] max-h-80 overflow-y-auto rounded-lg border border-border bg-card p-1 shadow-md"
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
                  <Check className={cn("h-3.5 w-3.5 shrink-0", !active && "invisible")} aria-hidden="true" />
                  <span className="truncate">{project.name}</span>
                </button>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}

/** The exit, as an icon in the sidebar's brand row. */
export function ForgeCloseButton({ onClose }: { onClose: () => void }) {
  const { isElectron, noDragRegionStyle } = useTitleBarChrome({ alignedToWindowEdge: false });
  const label = isElectron ? "Close Deployments" : "Back to app";
  return (
    <Tooltip content={`${label} (Esc)`} placement="bottom" delay={300}>
      <button
        type="button"
        onClick={onClose}
        style={noDragRegionStyle}
        className="inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted/70 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        aria-label={label}
        data-testid="forge-close"
      >
        {isElectron ? <X className="h-4 w-4" /> : <ArrowLeft className="h-4 w-4" />}
      </button>
    </Tooltip>
  );
}
