/**
 * `/m/projects` — which project the chat list shows: All projects, or one.
 *
 * The same model as the desktop sidebar's project menu: the project is a
 * filter on the chat list, not a place you have to enter. Picking a project
 * narrows the list to it (and moves the context there, as on desktop);
 * "All projects" lists every project's chats.
 *
 * Deliberately no create or import: both need a directory picker or a
 * repo-clone flow with more surface than a phone screen hosts well, and
 * onboarding creates the first project.
 *
 * A tap navigates at once and never waits on the switch (see `handleSelect`),
 * and skips the desktop workspace restore (`restoreWorkspace: false`).
 *
 * Each row's second line is where the project lives (hidden for a cloud
 * machine's checkout, which is the same path for every project), the third its
 * `last_active` in the chat list's compact age format.
 */

import { useNavigate } from "@tanstack/react-router";
import { Check, FolderGit2, Layers, Loader2 } from "lucide-react";
import { useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { useProjects } from "../../hooks/project-queries";
import { logger } from "../../lib/logger";
import { isCloudProjectPath } from "../Projects/picker/format";
import { PROJECT_DEFINITION_SHORT } from "../Projects/projectCopy";
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

/** The scope a row stands for: a project id, or null for All projects. */
type Scope = string | null;

export function MobileProjectList() {
  const navigate = useNavigate();
  const { projects, isLoading } = useProjects();
  const filterId = useWorkspaceStateStore((s) => s.projectFilterId);
  const setProjectFilter = useProjectStore((s) => s.setProjectFilter);

  // Navigate at once; never wait on the switch. setProjectFilter sets the
  // filter (and, through selectProject, the context project) synchronously,
  // before its first await, and the chat list scopes its own query by it — so
  // the list renders (with its own loading state) while the rest of the switch
  // runs. Awaiting it held the user on this screen behind a row spinner for
  // every serial RPC the switch made, with no timeout on any of them: on a
  // phone network, or a connection the OS had quietly dropped, the tap looked
  // like a frozen app.
  //
  // restoreWorkspace: false — the desktop workspace restore (last worktree,
  // last active chat) is not this surface's state; see SelectProjectOptions.
  const handleSelect = (scope: Scope) => {
    if (scope !== filterId) {
      setProjectFilter(scope, { restoreWorkspace: false }).catch((error: unknown) => {
        logger.warn("[MobileProjectList] Project filter switch failed", { scope, error });
      });
    }
    void navigate({ to: "/m/chats" });
  };

  const trailing = (scope: Scope) =>
    filterId === scope && <Check className="h-4 w-4 shrink-0 text-primary" />;

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
          title="No projects yet"
          description="Add a codebase on desktop — clone a repository or open a folder — and Reliant will start working in it."
        />
      ) : (
        <MobileScreenBody>
          <p className="px-1 pb-3 text-xs text-muted-foreground">{PROJECT_DEFINITION_SHORT}</p>
          <MobileCardGroup>
            <button
              type="button"
              onClick={() => handleSelect(null)}
              className={MOBILE_ROW}
              data-testid="mobile-project-all"
            >
              <MobileRowIcon icon={Layers} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium text-foreground">All projects</div>
                <div className="truncate text-xs text-muted-foreground">Every project's chats in one list</div>
              </div>
              {trailing(null)}
            </button>
          </MobileCardGroup>
          <MobileCardGroup>
            {projects.map((project) => (
              <button
                key={project.id}
                type="button"
                onClick={() => handleSelect(project.id)}
                className={MOBILE_ROW}
              >
                <MobileRowIcon icon={FolderGit2} />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium text-foreground">
                    {project.name}
                  </div>
                  {!isCloudProjectPath(project.path) && (
                    <div className="truncate text-xs text-muted-foreground">
                      {project.path}
                    </div>
                  )}
                  <div className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground/80">
                    {project.last_active && (
                      <span>Active {relativeTime(project.last_active)} ago</span>
                    )}
                  </div>
                </div>
                {trailing(project.id)}
              </button>
            ))}
          </MobileCardGroup>
        </MobileScreenBody>
      )}
    </div>
  );
}
