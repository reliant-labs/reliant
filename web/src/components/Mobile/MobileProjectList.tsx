/**
 * `/m/projects` — pick which project the chat list scopes to.
 *
 * Deliberately just a list + select: no create, no import. Both need a
 * directory picker or a repo-clone flow with more surface than a phone
 * screen can host well, and `MobileShell` already covers the case where a
 * brand-new user has zero projects (onboarding creates the first one).
 *
 * Selecting a project delegates to `projectStore.selectProject`, the same
 * call the desktop project switcher makes — it reloads chats and worktrees
 * for the new project and persists the choice, so returning to the chat list
 * is enough to see it re-scoped.
 *
 * Each row's second line is `worktree_count` + `last_active` — both already
 * on `projectStore`'s `Project` (see `ProjectPicker.tsx`'s desktop
 * equivalent), so this needed no new data, just surfacing what the row was
 * silently dropping. `relativeTime` is the same compact-age formatter the
 * chat list uses, for the same "now / 4m / 3h / 2d" shape across the surface.
 */

import { useEffect, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Check, FolderGit2, Loader2 } from "lucide-react";
import { useProjectStore, type Project } from "../../store/projectStore";
import { cn } from "../../lib/utils";
import { relativeTime } from "./relativeTime";
import {
  MOBILE_ROW,
  MobileBackButton,
  MobileCardGroup,
  MobileEmptyState,
  MobileRowIcon,
  MobileScreenBody,
  MobileScreenHeader,
} from "./MobileChrome";

export function MobileProjectList() {
  const navigate = useNavigate();
  const projects = useProjectStore((s) => s.projects);
  const currentProject = useProjectStore((s) => s.currentProject);
  const isLoading = useProjectStore((s) => s.isLoading);
  const loadProjects = useProjectStore((s) => s.loadProjects);
  const selectProject = useProjectStore((s) => s.selectProject);

  const [switchingId, setSwitchingId] = useState<string | null>(null);

  useEffect(() => {
    if (projects.length === 0) void loadProjects();
  }, [projects.length, loadProjects]);

  const handleSelect = async (project: Project) => {
    if (project.id === currentProject?.id) {
      await navigate({ to: "/m/chats" });
      return;
    }
    setSwitchingId(project.id);
    try {
      await selectProject(project);
      await navigate({ to: "/m/chats" });
    } finally {
      setSwitchingId(null);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileScreenHeader
        title="Projects"
        leading={
          <MobileBackButton
            onClick={() => void navigate({ to: "/m/chats" })}
            label="Back to chats"
          />
        }
      />

      {isLoading && projects.length === 0 ? (
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
      ) : projects.length === 0 ? (
        // No action: this surface has neither a directory picker nor a
        // repo-clone flow, so the only real next step is the desktop app.
        <MobileEmptyState
          icon={FolderGit2}
          title="No projects"
          description="Add a project on desktop and it will be available here."
        />
      ) : (
        <MobileScreenBody>
          <MobileCardGroup>
            {projects.map((project) => {
              const isCurrent = project.id === currentProject?.id;
              const isSwitching = switchingId === project.id;
              return (
                <button
                  key={project.id}
                  type="button"
                  disabled={isSwitching}
                  onClick={() => void handleSelect(project)}
                  className={cn(MOBILE_ROW, "disabled:opacity-60")}
                >
                  <MobileRowIcon icon={FolderGit2} />
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-medium text-foreground">
                      {project.name}
                    </div>
                    <div className="truncate text-xs text-muted-foreground">
                      {project.path}
                    </div>
                    <div className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground/80">
                      {typeof project.worktree_count === "number" && (
                        <span>
                          {project.worktree_count} workspace
                          {project.worktree_count === 1 ? "" : "s"}
                        </span>
                      )}
                      {typeof project.worktree_count === "number" &&
                        project.last_active && <span aria-hidden>·</span>}
                      {project.last_active && (
                        <span>Active {relativeTime(project.last_active)} ago</span>
                      )}
                    </div>
                  </div>
                  {isSwitching ? (
                    <Loader2 className="h-4 w-4 shrink-0 animate-spin text-muted-foreground" />
                  ) : (
                    isCurrent && <Check className="h-4 w-4 shrink-0 text-primary" />
                  )}
                </button>
              );
            })}
          </MobileCardGroup>
        </MobileScreenBody>
      )}
    </div>
  );
}
