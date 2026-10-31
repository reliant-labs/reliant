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
 */

import { useState } from "react";
import { toast } from "sonner";
import { ChevronDown, Download, Folder, FolderPlus, Loader2 } from "lucide-react";

import DropdownMenu, { type MenuGroup, type MenuItem } from "../forge-ui/dropdown_menu";
import Tooltip from "../forge-ui/tooltip";
import { createProjectFromRepo, useProjects, type Project } from "../../hooks/project-queries";
import { canChoose, canSetUpElsewhere, projectPlacement, type InstallRow } from "../../lib/projectPlacement";
import { openAddProject } from "../../store/addProjectDialogStore";
import { openNewChat } from "../../store/openChat";
import { useProjectStore } from "../../store/projectStore";
import { cn } from "../../lib/utils";
import { describeCloneError } from "../Projects/cloneTargets";

interface NewChatProjectChipProps {
  disabled?: boolean;
  /** The machine the user picked for this chat; absent when it is the default. */
  machine?: { daemonId: string; name: string };
  /** Every project_daemons row the user has (useProjectDaemonInstalls). */
  installs?: ReadonlyArray<InstallRow>;
}

export function NewChatProjectChip({ disabled = false, machine, installs = [] }: NewChatProjectChipProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const { projects } = useProjects();
  const [settingUp, setSettingUp] = useState<string | null>(null);
  if (!currentProject) return null;

  const recent = [...projects].sort(
    (a, b) => (b.last_active ?? "").localeCompare(a.last_active ?? "") || a.name.localeCompare(b.name),
  );
  const startIn = (project: Project) => {
    if (project.id !== currentProject.id) void openNewChat({ projectId: project.id });
  };
  const choice = (project: Project): MenuItem => ({
    label: project.name,
    checked: project.id === currentProject.id,
    onClick: () => startIn(project),
    testId: `new-chat-project-${project.id}`,
  });

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

  let groups: MenuGroup[];
  if (machine) {
    const onMachine: Project[] = [];
    const elsewhere: Project[] = [];
    for (const project of recent) {
      if (canChoose(projectPlacement(installs, project.id), machine.daemonId)) onMachine.push(project);
      else elsewhere.push(project);
    }
    groups = [
      ...(onMachine.length > 0
        ? [{ label: `On ${machine.name}`, selection: "single" as const, items: onMachine.map(choice) }]
        : []),
      ...(elsewhere.length > 0
        ? [
            {
              label: `Not set up on ${machine.name}`,
              items: elsewhere.map((project): MenuItem => {
                const cloneable = canSetUpElsewhere(project);
                return {
                  label: cloneable ? `Set up ${project.name} on ${machine.name}` : project.name,
                  icon:
                    settingUp === project.id ? (
                      <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" />
                    ) : cloneable ? (
                      <Download className="h-3.5 w-3.5" />
                    ) : undefined,
                  hint: cloneable ? undefined : "Folder elsewhere",
                  disabled: !cloneable || settingUp !== null,
                  onClick: cloneable ? () => void setUpOn(project, machine) : undefined,
                  testId: `new-chat-project-set-up-${project.id}`,
                };
              }),
            },
          ]
        : []),
    ];
  } else {
    groups = [{ label: "Start in", selection: "single", items: recent.map(choice) }];
  }
  groups.push({
    items: [
      {
        label: "Add project…",
        icon: <FolderPlus className="h-3.5 w-3.5" />,
        onClick: () => openAddProject(),
        testId: "new-chat-project-add",
      },
    ],
  });

  return (
    <DropdownMenu
      align="left"
      label="Project"
      disabled={disabled}
      menuClassName="max-h-80 w-72 overflow-y-auto"
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
      groups={groups}
    />
  );
}
