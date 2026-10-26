/**
 * `/m/projects` — pick which project the chat list scopes to.
 *
 * Deliberately just a list + select: no create, no import. Both need a
 * directory picker or a repo-clone flow with more surface than a phone
 * screen can host well, and `MobileShell` already covers the case where a
 * brand-new user has zero projects (onboarding creates the first one).
 *
 * Selecting a project delegates to `projectStore.selectProject`, the same
 * call the desktop project switcher makes — it reloads chats for the new
 * project and persists the choice, so returning to the chat list is enough to
 * see it re-scoped. It skips the desktop workspace restore
 * (`restoreWorkspace: false`) and is not awaited: see `handleSelect`.
 *
 * Each row's second line is the project's `last_active`. `relativeTime` is
 * the same compact-age formatter the chat list uses, for the same
 * "now / 4m / 3h / 2d" shape across the surface.
 */

import { useNavigate } from "@tanstack/react-router";
import { Check, FolderGit2, Loader2 } from "lucide-react";
import { useProjectStore, type Project } from "../../store/projectStore";
import { useProjects } from "../../hooks/project-queries";
import { logger } from "../../lib/logger";
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
  const { projects, isLoading } = useProjects();
  const currentProject = useProjectStore((s) => s.currentProject);
  const selectProject = useProjectStore((s) => s.selectProject);

  // Navigate at once; never wait on the switch. selectProject makes the new
  // project current synchronously, before its first await, and the chat list
  // scopes its own query by that id — so the list can render (with its own
  // loading state) while the rest of the switch runs. Awaiting it held the
  // user on this screen behind a row spinner for every serial RPC the switch
  // made, with no timeout on any of them: on a phone network, or a connection
  // the OS had quietly dropped, the tap looked like a frozen app.
  //
  // restoreWorkspace: false — the desktop workspace restore (last worktree,
  // last active chat) is not this surface's state; see SelectProjectOptions.
  const handleSelect = (project: Project) => {
    if (project.id !== currentProject?.id) {
      selectProject(project, { restoreWorkspace: false }).catch((error: unknown) => {
        logger.warn("[MobileProjectList] Project switch failed", { projectId: project.id, error });
      });
    }
    void navigate({ to: "/m/chats" });
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
              return (
                <button
                  key={project.id}
                  type="button"
                  onClick={() => handleSelect(project)}
                  className={MOBILE_ROW}
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
                      {project.last_active && (
                        <span>Active {relativeTime(project.last_active)} ago</span>
                      )}
                    </div>
                  </div>
                  {isCurrent && <Check className="h-4 w-4 shrink-0 text-primary" />}
                </button>
              );
            })}
          </MobileCardGroup>
        </MobileScreenBody>
      )}
    </div>
  );
}
