// Copyright (c) 2025 Reliant Labs

/**
 * "Move to project…": moves a chat to another of the user's projects
 * (MoveChatToProject). The chat keeps its history; its next turn runs in the
 * new project's main checkout, on a machine that has the project. The server
 * leaves the model a note that earlier messages name the old checkout's paths.
 *
 * Only an idle chat (or one stopped on an error) moves, and a chat in a branch
 * workspace moves only by leaving the workspace behind: the workspace belongs
 * to its project. The dialog says both up front rather than letting the
 * server refuse.
 */

import { useEffect, useMemo, useState } from "react";
import Modal from "../forge-ui/modal";
import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import { useChat, useMoveChatToProject } from "@/hooks/chat-queries";
import { useProjectDaemonInstalls, useProjects, type ProjectDaemonInfo } from "@/hooks/project-queries";
import { ChatActivity } from "@/gen/reliant/v1/chat_pb";
import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { useChatStore } from "@/store/chatStore";
import { openChat } from "@/store/openChat";
import { toast } from "@/lib/toast-manager";
import { cn } from "@/lib/utils";

interface MoveChatToProjectDialogProps {
  chatId: string;
  open: boolean;
  onClose: () => void;
}

/** Where the chat would run in a project, for one row of the picker. */
export interface MoveTarget {
  /** The server would accept the move. */
  movable: boolean;
  hint: string;
}

/**
 * The machine a project's row promises, by the server's rule: the chat's own
 * machine when it has the project (or the project is installed nowhere, so any
 * machine is accepted), else one that has it, else none — a project still
 * being set up everywhere cannot take the chat yet.
 */
export function moveTarget(
  installs: ReadonlyArray<Pick<ProjectDaemonInfo, "project_id" | "daemon_id" | "install_state">>,
  projectId: string,
  chat: { activeDaemonId?: string; noMachine?: boolean },
): MoveTarget {
  if (chat.noMachine) return { movable: true, hint: "Stays without a machine" };
  const rows = installs.filter((row) => row.project_id === projectId);
  if (rows.length === 0) return { movable: true, hint: "Not set up on a machine yet" };
  const ready = rows.filter((row) => row.install_state === ProjectInstallState.INSTALLED);
  if (chat.activeDaemonId && ready.some((row) => row.daemon_id === chat.activeDaemonId)) {
    return { movable: true, hint: "On this chat's machine" };
  }
  if (ready.length > 0) return { movable: true, hint: "Moves to a machine that has it" };
  return { movable: false, hint: "Not set up on any of your machines yet" };
}

/** The chat may move now: idle, or stopped on an error. */
export function canMoveNow(activity: ChatActivity | undefined): boolean {
  return activity === undefined || activity === ChatActivity.IDLE || activity === ChatActivity.ERROR;
}

export function MoveChatToProjectDialog({ chatId, open, onClose }: MoveChatToProjectDialogProps) {
  const chat = useChat(open ? chatId : undefined).data;
  const { projects, isLoading } = useProjects();
  const { data: installs = [] } = useProjectDaemonInstalls(open);
  const move = useMoveChatToProject();
  const [selected, setSelected] = useState<string | undefined>(undefined);
  const [filter, setFilter] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    if (open) {
      setSelected(undefined);
      setFilter("");
      setError("");
    }
  }, [open]);

  const candidates = useMemo(() => {
    const needle = filter.trim().toLowerCase();
    return projects
      .filter((project) => project.id !== chat?.projectId)
      .filter((project) => !needle || project.name.toLowerCase().includes(needle))
      .map((project) => ({
        project,
        target: moveTarget(installs, project.id, { activeDaemonId: chat?.activeDaemonId, noMachine: chat?.noMachine }),
      }));
  }, [projects, installs, chat?.projectId, chat?.activeDaemonId, chat?.noMachine, filter]);

  const workspace = chat?.worktree && !chat.worktree.isMain ? chat.worktree : undefined;
  const busy = !!chat && !canMoveNow(chat.activity);
  const choice = candidates.find((c) => c.project.id === selected && c.target.movable);
  const currentProjectName = projects.find((p) => p.id === chat?.projectId)?.name ?? "its project";

  const confirm = async () => {
    if (!choice) return;
    setError("");
    try {
      const moved = await move.mutateAsync({
        chatId,
        projectId: choice.project.id,
        leaveWorkspace: !!workspace,
      });
      toast.success(`Moved to ${choice.project.name}`);
      onClose();
      // The open chat follows itself: its project and workspace context
      // switch to where it now lives.
      if (useChatStore.getState().activeChatId === chatId) {
        void openChat(moved);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not move the chat");
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Move to project"
      description="The chat keeps its history and continues in the project you pick. Paths from earlier messages belong to the old project; the assistant is told so."
      size="md"
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={move.isPending}>
            Cancel
          </Button>
          <Button
            onClick={() => void confirm()}
            disabled={!choice || busy || move.isPending}
            data-testid="move-chat-confirm"
          >
            {move.isPending ? "Moving…" : workspace ? "Leave workspace and move" : "Move"}
          </Button>
        </div>
      }
    >
      <div className="space-y-3 px-6 py-4">
        {busy && (
          <p className="rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-muted-foreground" role="status">
            This chat is busy. Wait for it to finish, or stop it, before moving it.
          </p>
        )}
        {workspace && (
          <p className="rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-muted-foreground" data-testid="move-chat-workspace-notice">
            This chat runs in workspace <span className="font-medium text-foreground">{workspace.branch || workspace.name}</span>,
            which stays with {currentProjectName}. The chat continues in the new project&apos;s main checkout.
          </p>
        )}
        {projects.length > 6 && (
          <Input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Filter projects"
            aria-label="Filter projects"
          />
        )}
        {isLoading ? (
          <p className="text-sm text-muted-foreground">Loading your projects…</p>
        ) : candidates.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {filter ? "No project matches." : "You have no other project to move this chat to."}
          </p>
        ) : (
          <div role="radiogroup" aria-label="Project" className="max-h-80 space-y-1.5 overflow-y-auto">
            {candidates.map(({ project, target }) => {
              const checked = selected === project.id;
              return (
                <button
                  key={project.id}
                  type="button"
                  role="radio"
                  aria-checked={checked}
                  disabled={!target.movable}
                  onClick={() => setSelected(project.id)}
                  className={cn(
                    "w-full rounded-md border px-3 py-2 text-left transition-colors",
                    "bg-background border-border/60 hover:border-border",
                    "disabled:cursor-not-allowed disabled:opacity-60",
                    checked && "border-primary ring-1 ring-primary/40",
                  )}
                >
                  <div className="truncate text-sm font-medium text-foreground">{project.name}</div>
                  <div className="text-xs text-muted-foreground">{target.hint}</div>
                </button>
              );
            })}
          </div>
        )}
        {error && <p className="text-xs text-destructive-ink">{error}</p>}
      </div>
    </Modal>
  );
}
