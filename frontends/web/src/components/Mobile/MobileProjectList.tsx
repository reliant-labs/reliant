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
 * The choice lives in the URL, the chat list's `?projects=<id>|all`
 * (components/Mobile/mobileScope.ts): a tap only navigates there, and the
 * stores follow (MobileScopeSync).
 *
 * Each row's second line is where the project lives (hidden for a cloud
 * machine's checkout, which is the same path for every project), the third its
 * `last_active` in the chat list's compact age format.
 */

import { useNavigate } from "@tanstack/react-router";
import { Check, FolderGit2, Layers, Loader2 } from "lucide-react";
import { useProjects } from "../../hooks/project-queries";
import { shellScopeSearch } from "../Layout/shellScopeNav";
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
import { mobileChatsPath } from "@/lib/paths";
import { useMobileChatScope } from "./mobileScope";

/** The scope a row stands for: a project id, or null for All projects. */
type Scope = string | null;

export function MobileProjectList() {
  const navigate = useNavigate();
  const { projects, isLoading } = useProjects();
  const filterId = useMobileChatScope();

  // The pick is the URL's (`?projects=<id>|all`): a push, so Back undoes it,
  // and a reload or a shared link keeps it. The list scopes its own query by
  // the URL, and MobileScopeSync moves the context project after it, so the
  // tap navigates at once and never waits on a switch.
  const handleSelect = (scope: Scope) => {
    const shown = shellScopeSearch(scope ? { mode: "one", projectId: scope } : { mode: "all" });
    void navigate({ to: mobileChatsPath(), search: ((prev: Record<string, unknown>) => ({ ...prev, ...shown })) as never });
  };

  const trailing = (scope: Scope) =>
    filterId === scope && <Check className="h-4 w-4 shrink-0 text-primary-ink" />;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileScreenHeader
        title="Projects"
        leading={
          <MobileBackButton
            onClick={() => void navigate({ to: mobileChatsPath() })}
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
          <p className="px-1 pb-3 text-meta text-muted-foreground">{PROJECT_DEFINITION_SHORT}</p>
          <MobileCardGroup>
            <button
              type="button"
              onClick={() => handleSelect(null)}
              className={MOBILE_ROW}
              data-testid="mobile-project-all"
            >
              <MobileRowIcon icon={Layers} />
              <div className="min-w-0 flex-1">
                <div className="truncate text-ui font-medium text-foreground">All projects</div>
                <div className="truncate text-meta text-muted-foreground">Every project's chats in one list</div>
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
                  <div className="truncate text-ui font-medium text-foreground">
                    {project.name}
                  </div>
                  {!isCloudProjectPath(project.path) && (
                    <div className="truncate text-meta text-muted-foreground">
                      {project.path}
                    </div>
                  )}
                  <div className="mt-0.5 flex items-center gap-1.5 text-meta text-muted-foreground/80">
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
