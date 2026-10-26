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
 * Each row's second line is where the project lives (hidden for a cloud
 * machine's checkout, which is the same path for every project), the third its
 * `last_active` in the chat list's compact age format.
 */

import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Check, FolderGit2, Layers, Loader2 } from "lucide-react";
import { useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { useProjects } from "../../hooks/project-queries";
import { cn } from "../../lib/utils";
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

  const [switching, setSwitching] = useState<Scope | undefined>(undefined);

  const handleSelect = async (scope: Scope) => {
    if (scope === filterId) {
      await navigate({ to: "/m/chats" });
      return;
    }
    setSwitching(scope);
    try {
      await setProjectFilter(scope);
      await navigate({ to: "/m/chats" });
    } finally {
      setSwitching(undefined);
    }
  };

  const trailing = (scope: Scope) =>
    switching === scope ? (
      <Loader2 className="h-4 w-4 shrink-0 animate-spin text-muted-foreground" />
    ) : (
      filterId === scope && <Check className="h-4 w-4 shrink-0 text-primary" />
    );

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
              disabled={switching !== undefined}
              onClick={() => void handleSelect(null)}
              className={cn(MOBILE_ROW, "disabled:opacity-60")}
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
                disabled={switching !== undefined}
                onClick={() => void handleSelect(project.id)}
                className={cn(MOBILE_ROW, "disabled:opacity-60")}
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
