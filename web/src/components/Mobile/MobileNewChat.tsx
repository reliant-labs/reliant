/** Mobile chat creation shares the main composer; machine and workspace selection stay in this screen. */

import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ChevronLeft, GitBranchPlus, Workflow, Monitor, FolderGit2 } from "lucide-react";
import { useChatStore } from "../../store/chatStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { sortActiveWorkspaces } from "../Worktrees/workspaceStatus";
import { ChatInput } from "../Chat/ChatInput";
import { useChatParamsStore } from "../../store/chatParamsStore";
import { useWorkflows } from "../../store/globalDataStore";
import { usePreferencesStore, DEFAULT_WORKFLOW } from "../../store/preferencesStore";
import { isChatLaunchable } from "../../api/workflow-grpc";
import { normalizeWorkflowRef } from "../workflow/useWorkflowInputs";
import { workflowDisplayName } from "../../lib/workflowDisplayName";
import { trackEvent } from "../../lib/analytics";
import { MobileCardGroup, MobileScreenHeader } from "./MobileChrome";
import { MobileSelectRow, type MobileSettingOption } from "./MobileSettingsRow";
import { MobileCreateWorkspaceSheet } from "./MobileCreateWorkspaceSheet";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import { useCapability } from "@/lib/surfaceContext";
import {
  DEFAULT_MACHINE,
  NO_MACHINE,
  NO_MACHINE_COMPOSER_HINT,
  chatMachineOptions,
  defaultChatMachine,
  defaultMachineDaemon,
  startOptionsForMachine,
} from "@/lib/chatMachine";

/**
 * Workspaces a chat can start in: main first, then most recently active.
 * FAILED ones are left out because they never got a checkout. CREATING ones
 * stay in: a chat can start while its checkout finishes, which is what the
 * desktop composer allows straight after create too.
 */
export function workspaceOptions(worktrees: Worktree[]): MobileSettingOption[] {
  return sortActiveWorkspaces(
    worktrees.filter((w) => !w.deleted_at && w.status !== WorktreeStatus.FAILED),
  ).map((w) => ({
    value: w.id,
    label: w.name,
    description:
      w.status === WorktreeStatus.CREATING
        ? "Setting up…"
        : w.is_main
          ? `${w.branch} · main checkout`
          : w.branch,
  }));
}

/** Short, user-facing summaries for the mobile workflow chooser. */
function mobileWorkflowSummary(label: string, description?: string): string | undefined {
  const summaries: Record<string, string> = {
    "agent": "Chat, code, and get things done.",
    "best of three": "Three agents work in parallel; choose the best result.",
    "create a workflow": "Build a reusable workflow through chat.",
  };
  const summary = summaries[label.toLowerCase()];
  if (summary) return summary;
  const firstSentence = description?.split(/(?<=[.!?])\s|\n/)[0]?.trim();
  if (!firstSentence) return undefined;
  return firstSentence.length > 90 ? `${firstSentence.slice(0, 87).replace(/\s+\S*$/, "")}…` : firstSentence;
}

export function MobileNewChat() {
  const navigate = useNavigate();
  // `strict: false` — nested under `_authenticated` → `_mobile`, same as
  // every other param/search read on this surface.
  const { worktreeId: requestedWorktreeId } = useSearch({ strict: false }) as {
    worktreeId?: string;
  };
  const [isCreating, setIsCreating] = useState(false);
  const [error, setError] = useState("");

  const { workflows } = useWorkflows();
  const defaultWorkflow = usePreferencesStore(state => state.preferences?.defaultWorkflow) ?? DEFAULT_WORKFLOW;
  const isWorkflowHidden = usePreferencesStore(state => state.isWorkflowHidden);
  const selectedWorkflow = useChatParamsStore(state => state.tempNewChatWorkflow);
  const effectiveWorkflow = selectedWorkflow ?? defaultWorkflow;
  const workflowOptions = useMemo(() => {
    const seen = new Set<string>();
    return workflows.filter(workflow => {
      const name = normalizeWorkflowRef(workflow.name);
      if (seen.has(name) || isWorkflowHidden(workflow.name) || !isChatLaunchable(workflow)) return false;
      seen.add(name);
      return true;
    }).map(workflow => ({ value: workflow.name, label: workflowDisplayName(workflow), description: mobileWorkflowSummary(workflowDisplayName(workflow), workflow.description) }));
  }, [workflows, isWorkflowHidden]);
  const workflowRowValue = workflowOptions.find(option => normalizeWorkflowRef(option.value) === normalizeWorkflowRef(effectiveWorkflow))?.value ?? effectiveWorkflow;
  const changeWorkflow = (value: string) => {
    const store = useChatParamsStore.getState();
    store.setTempNewChatParams({});
    store.setTempNewChatPresets({});
    store.setTempNewChatWorkflow(value);
  };

  const currentProject = useProjectStore((s) => s.currentProject);
  const worktrees = useWorktreeStore((s) => s.worktrees);
  const loadWorktrees = useWorktreeStore((s) => s.loadWorktrees);

  // MobileShell guarantees a project but not its worktrees; without this the
  // main worktree is missing and every send would fail on a phone that landed
  // here directly instead of via the chat list.
  useEffect(() => {
    if (!currentProject) return;
    if (worktrees.length === 0) void loadWorktrees(currentProject.id);
  }, [currentProject, worktrees.length, loadWorktrees]);

  // The user's pick on this screen wins over the one the URL arrived with.
  const [pickedWorktreeId, setPickedWorktreeId] = useState<string | null>(null);
  const [creatingWorkspace, setCreatingWorkspace] = useState(false);
  const canCreateWorkspace = useCapability("worktreeCreate");

  const mainWorktree = worktrees.find((w) => w.is_main && !w.deleted_at);
  const wantedWorktreeId = pickedWorktreeId ?? requestedWorktreeId;
  const requestedWorktree = wantedWorktreeId
    ? worktrees.find((w) => w.id === wantedWorktreeId && !w.deleted_at)
    : undefined;
  const targetWorktree = requestedWorktree ?? mainWorktree;
  const workspaceRowOptions = useMemo(() => workspaceOptions(worktrees), [worktrees]);

  const canPickMachine = useCapability("chatDaemonSelection");
  const { data: daemons = [], isLoading: daemonsLoading } = useDaemonList();
  const [selectedMachine, setSelectedMachine] = useState<string | null>(null);
  const machine =
    selectedMachine ?? defaultChatMachine({ daemons, loading: daemonsLoading, surface: "mobile" }) ?? DEFAULT_MACHINE;
  const noMachine = machine === NO_MACHINE;
  // DEFAULT_MACHINE sends no daemon (the server picks); show the machine it
  // will land on — an awake one, or one still starting.
  const machineRowValue = machine === DEFAULT_MACHINE ? (defaultMachineDaemon(daemons)?.daemonId ?? "") : machine;
  const machineRowOptions = [
    ...chatMachineOptions(daemons).map((option) => ({
      value: option.value,
      label: option.label,
      description: option.statusLabel === "suspended" ? "suspended · wakes when you send" : option.statusLabel,
    })),
    { value: NO_MACHINE, label: "No machine", description: "Web & integrations · can't read or change files" },
  ];
  const handleSend = async (
    message: string,
    attachmentIds?: string[],
    workflow?: string | null,
    workflowParams?: Record<string, unknown>,
    _targetThread?: string | null,
    selectedPresets?: Record<string, string | null>,
  ) => {
    const content = message.trim();
    if ((!content && !attachmentIds?.length) || isCreating || !currentProject) return;

    setIsCreating(true);
    setError("");
    try {
      const { daemon_id: daemonId, no_machine: startNoMachine } = startOptionsForMachine(machine);
      // A chat with no machine runs in the main checkout: a branch workspace
      // is a checkout on one machine.
      const worktreeId = noMachine ? mainWorktree?.id : targetWorktree?.id;
      const chat = await useChatStore
        .getState()
        .startChat(
          worktreeId,
          content,
          attachmentIds,
          workflowParams,
          workflow,
          selectedPresets ? Object.fromEntries(Object.entries(selectedPresets).filter((entry): entry is [string, string] => entry[1] !== null)) : undefined,
          daemonId || startNoMachine ? { daemonId, noMachine: startNoMachine } : undefined,
        );

      trackEvent("chat_created", {
        has_attachments: !!attachmentIds?.length,
        workflow: workflow ?? "default",
        surface: "mobile",
        no_machine: noMachine,
      });

      // Mirror the desktop path: the chat screen renders ChatContainer, which
      // reads activeChatId for its subscriptions.
      useChatParamsStore.getState().transferTempToChat(chat.id);
      useChatStore.getState().selectChat(chat);

      await navigate({ to: "/m/chats/$chatId", params: { chatId: chat.id } });
    } catch (err) {
      // Stay on the screen with the text intact — retyping a prompt on a
      // phone is the worst possible recovery.
      setError(
        err instanceof Error ? err.message : "Could not start the chat",
      );
      setIsCreating(false);
      throw err;
    }
  };

  const startOptions = (
    <MobileCardGroup label="Start with">
        <MobileSelectRow
          label="Workflow"
          value={workflowRowValue}
          options={workflowOptions}
          onChange={changeWorkflow}
          sheetTitle="Choose a workflow"
          icon={<Workflow className="h-4 w-4" />}
        />
        {canPickMachine && !daemonsLoading && (
          <MobileSelectRow
            label="Runs on"
            icon={<Monitor className="h-4 w-4" />}
            value={machineRowValue}
            sheetTitle="Where this chat runs"
            options={machineRowOptions}
            onChange={setSelectedMachine}
          />
        )}
        {/* Shown only when there is something to do with it: another
            workspace to pick, or one to create. A row with main as the
            only choice is just noise above the composer. */}
        {!noMachine &&
          workspaceRowOptions.length > 0 &&
          (workspaceRowOptions.length > 1 || canCreateWorkspace) && (
            <MobileSelectRow
              label="Workspace"
              icon={<FolderGit2 className="h-4 w-4" />}
              value={targetWorktree?.id ?? ""}
              sheetTitle="Which workspace"
              options={workspaceRowOptions}
              onChange={setPickedWorktreeId}
              action={
                canCreateWorkspace
                  ? {
                      label: "New workspace",
                      icon: <GitBranchPlus className="h-4 w-4" />,
                      onSelect: () => setCreatingWorkspace(true),
                    }
                  : undefined
              }
            />
          )}
    </MobileCardGroup>
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileScreenHeader
        title="New chat"
        titleClassName="text-base font-semibold"
        subtitle={currentProject?.name ?? "No project"}
        leading={
          <Link
            to="/m/chats"
            // Explicit px, not `h-10 w-10`: rem sizing resolves against the
            // root font-size, and at the smallest Appearance step `h-10`
            // measures under 44px — on the only way out of this screen.
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
            aria-label="Back to chats"
          >
            <ChevronLeft className="h-5 w-5" />
          </Link>
        }
      />

      <div className="relative isolate min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pt-3 pb-4">
        <div className="mx-auto w-full max-w-[760px]">
        {startOptions}
        {noMachine && <p role="note" className="mt-3 px-1 text-xs leading-relaxed text-muted-foreground">{NO_MACHINE_COMPOSER_HINT}</p>}
        </div>
      </div>

      {error && (
        <p className="shrink-0 px-4 py-2 text-xs text-destructive-ink">{error}</p>
      )}

      <div className="shrink-0" style={{ paddingBottom: "var(--mobile-bottom-inset, env(safe-area-inset-bottom))" }}>
        <ChatInput onSend={handleSend} disabled={isCreating || !currentProject} />
      </div>

      {creatingWorkspace && currentProject && (
        <MobileCreateWorkspaceSheet
          projectId={currentProject.id}
          onClose={() => setCreatingWorkspace(false)}
          onCreated={(worktree) => {
            // The store has already appended it, so selecting it by id
            // resolves on the next render.
            setPickedWorktreeId(worktree.id);
            setCreatingWorkspace(false);
          }}
        />
      )}
    </div>
  );
}
