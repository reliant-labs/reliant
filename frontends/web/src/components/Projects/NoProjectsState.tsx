// Copyright (c) 2025 Reliant Labs

/**
 * First run: the account has no projects yet. The only time the chat window
 * shows a projects screen instead of the chat shell — with one project or
 * more, the window always lands on a project and switching is the sidebar's
 * project menu.
 *
 * It says what a project is (projectCopy), offers the two ways to add one, and
 * — only when it blocks adding a folder — the state of the user's machines.
 */

import { FolderGit2 } from "lucide-react";

import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { openAddProject } from "../../store/addProjectDialogStore";
import { AddProjectOptions } from "./AddProjectOptions";
import { NoActiveMachinePanel } from "./picker/MachineStatus";
import { NO_PROJECTS_COPY, PROJECT_DEFINITION } from "./projectCopy";
import { useAddProject } from "./useAddProject";

export function NoProjectsState() {
  const isElectron = !!window.electronAPI?.selectDirectory;
  const { activeDaemon, loading: daemonLoading } = useDaemonStatus();
  const { actions } = useAddProject({
    onChooseClone: () => openAddProject("clone"),
    onChooseFolderInBrowser: () => openAddProject("folder"),
  });
  // A machine matters here only when its absence blocks something: in the
  // browser, a folder can only be opened on an attached machine.
  const showMachinePanel = !isElectron && !activeDaemon && !daemonLoading;

  return (
    <div className="forge-ui h-full overflow-y-auto bg-background" data-testid="no-projects-state">
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-6 px-6 py-12">
        <header>
          <h1 className="text-xl font-semibold text-ink">Projects</h1>
          <p className="mt-1 max-w-prose text-pretty text-sm text-ink-muted">{PROJECT_DEFINITION}</p>
        </header>

        {showMachinePanel && <NoActiveMachinePanel />}

        <section
          className="rounded-lg border border-dashed border-border-strong bg-card px-6 py-8"
          aria-labelledby="no-projects-title"
        >
          <div className="mx-auto mb-6 max-w-md text-center">
            <FolderGit2 className="mx-auto mb-3 h-6 w-6 text-ink-muted" aria-hidden="true" />
            <h2 id="no-projects-title" className="text-sm font-semibold text-ink">
              No projects yet
            </h2>
            <p className="mt-1 text-pretty text-sm text-ink-muted">{NO_PROJECTS_COPY}</p>
          </div>
          <AddProjectOptions actions={actions} />
        </section>
      </div>
    </div>
  );
}
