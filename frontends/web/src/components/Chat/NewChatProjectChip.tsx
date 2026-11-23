// Copyright (c) 2025 Reliant Labs

/**
 * The new chat's project, as a chip beside "Runs on" and the workspace.
 *
 * In the global window "New chat" is not tied to a project you first had to
 * enter: it starts in the sidebar filter's project, else the open chat's,
 * else the last one used (store/openChat newChatTargetProjectId). This chip
 * shows which, and changes it inline — which moves the new chat to that
 * project's main workspace. With no other project to pick it offers Add
 * project.
 *
 * When the user picked the machine first (`machine`), the projects are split
 * the way the machine picker splits machines (lib/projectPlacement.ts): those
 * on that machine can be chosen; every other one is offered as "Set up
 * <project> on <machine>", which clones it there and then starts in it. A
 * project that is a plain folder elsewhere cannot be cloned, so it is listed
 * disabled with the reason. No pick here leads to a chat that cannot start.
 *
 * The list is the project selector (Projects/ProjectSelector, chat mode:
 * projects only), so it searches, pins and scales like every other picker;
 * its footer's New project is the old Add project.
 */

import { useCallback, useState } from "react";
import { toast } from "sonner";
import { ChevronDown, Folder } from "lucide-react";

import Tooltip from "../forge-ui/tooltip";
import { createProjectFromRepo, type Project } from "../../hooks/project-queries";
import { canChoose, canSetUpElsewhere, projectPlacement, type InstallRow } from "../../lib/projectPlacement";
import { openNewChat } from "../../store/openChat";
import { useProjectStore } from "../../store/projectStore";
import { cn } from "../../lib/utils";
import { describeCloneError } from "../Projects/cloneTargets";
import { ProjectSelector, type ProjectRowState, type ProjectSelection } from "../Projects/ProjectSelector";
import type { ProjectCatalogRow } from "../../services/projects/catalog";

interface NewChatProjectChipProps {
  disabled?: boolean;
  /** The machine the user picked for this chat; absent when it is the default. */
  machine?: { daemonId: string; name: string };
  /** Every project_daemons row the user has (useProjectDaemonInstalls). */
  installs?: ReadonlyArray<InstallRow>;
}

export function NewChatProjectChip({ disabled = false, machine, installs = [] }: NewChatProjectChipProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const [settingUp, setSettingUp] = useState<string | null>(null);

  // On a picked machine: a project not set up there is offered as "Set up
  // on <machine>" when it can be cloned, and is disabled when it cannot.
  const rowState = useCallback(
    (row: ProjectCatalogRow): ProjectRowState | undefined => {
      if (!machine || canChoose(projectPlacement(installs, row.key), machine.daemonId)) return undefined;
      if (!canSetUpElsewhere(row.project)) return { hint: "Folder elsewhere", disabled: true };
      if (settingUp === row.key) return { hint: `Setting up on ${machine.name}…`, disabled: true };
      return { hint: `Set up on ${machine.name}`, disabled: settingUp !== null };
    },
    [machine, installs, settingUp],
  );

  if (!currentProject) return null;

  const startIn = (project: Project) => {
    if (project.id !== currentProject.id) void openNewChat({ projectId: project.id });
  };

  const setUpOn = async (project: Project, target: { daemonId: string; name: string }) => {
    setSettingUp(project.id);
    try {
      await createProjectFromRepo({
        cloneUrl: project.remote_url ?? "",
        daemonId: target.daemonId,
        name: project.name,
        path: project.path,
      });
      // The clone is queued; the new chat waits on that machine for it.
      startIn(project);
    } catch (err) {
      toast.error(`Couldn't set up ${project.name} on ${target.name}`, {
        description: describeCloneError(err, target.name),
      });
    } finally {
      setSettingUp(null);
    }
  };

  const onSelect = (selection: ProjectSelection) => {
    if (selection.kind !== "project") return;
    const { project } = selection.row;
    if (machine && !canChoose(projectPlacement(installs, project.id), machine.daemonId)) void setUpOn(project, machine);
    else startIn(project);
  };

  return (
    <ProjectSelector
      mode="chat"
      align="left"
      disabled={disabled}
      selectedKey={currentProject.id}
      rowState={rowState}
      onSelect={onSelect}
      trigger={(props) => (
        <Tooltip content="The project this chat starts in" side="top">
          <button
            type="button"
            {...props}
            disabled={disabled}
            data-testid="new-chat-project-chip"
            aria-label={`Project: ${currentProject.name}. Change project`}
            className={cn(
              "flex items-center gap-2 rounded-lg border border-border/70 bg-muted/40 px-5 py-2.5 text-sm font-medium text-foreground",
              "transition-colors hover:border-border hover:bg-muted/60 disabled:cursor-not-allowed disabled:opacity-60",
            )}
          >
            <Folder className="h-4 w-4" aria-hidden="true" />
            <span className="max-w-[180px] truncate">{currentProject.name}</span>
            <ChevronDown className="h-3.5 w-3.5 opacity-60" aria-hidden="true" />
          </button>
        </Tooltip>
      )}
    />
  );
}
