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
 */

import { ChevronDown, Folder, FolderPlus } from "lucide-react";

import DropdownMenu, { type MenuGroup } from "../forge-ui/dropdown_menu";
import Tooltip from "../forge-ui/tooltip";
import { useProjects } from "../../hooks/project-queries";
import { openAddProject } from "../../store/addProjectDialogStore";
import { openNewChat } from "../../store/openChat";
import { useProjectStore } from "../../store/projectStore";
import { cn } from "../../lib/utils";

export function NewChatProjectChip({ disabled = false }: { disabled?: boolean }) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const { projects } = useProjects();
  if (!currentProject) return null;

  const recent = [...projects].sort(
    (a, b) => (b.last_active ?? "").localeCompare(a.last_active ?? "") || a.name.localeCompare(b.name),
  );
  const groups: MenuGroup[] = [
    {
      label: "Start in",
      selection: "single",
      items: recent.map((project) => ({
        label: project.name,
        checked: project.id === currentProject.id,
        onClick: () => {
          if (project.id !== currentProject.id) void openNewChat({ projectId: project.id });
        },
        testId: `new-chat-project-${project.id}`,
      })),
    },
    {
      items: [
        {
          label: "Add project…",
          icon: <FolderPlus className="h-3.5 w-3.5" />,
          onClick: () => openAddProject(),
          testId: "new-chat-project-add",
        },
      ],
    },
  ];

  return (
    <DropdownMenu
      align="left"
      label="Project"
      disabled={disabled}
      menuClassName="max-h-80 w-64 overflow-y-auto"
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
