import { logger } from "../../lib/logger";
import { useState, useEffect, useRef, type ReactNode } from "react";
import { AddMachineSheet } from "../Machines/AddMachineSheet";
import { useChatStore } from "../../store/chatStore"; // For getState() only
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useAttachmentStore } from "../../store/attachmentStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { useChatParamsStore } from "../../store/chatParamsStore";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useDaemonWait } from "@/hooks/useDaemonWait";
import { useBundledDaemonPending } from "@/hooks/useBundledDaemonPending";
import { useCapability } from "@/lib/surfaceContext";
import {
  DEFAULT_MACHINE,
  NO_MACHINE,
  NO_MACHINE_COMPOSER_HINT,
  defaultChatMachine,
  defaultMachineDaemon,
  hasMachine,
  startOptionsForMachine,
  type ChatMachineChoice,
} from "@/lib/chatMachine";
import { createProjectFromRepo, useProjectDaemonInstalls } from "@/hooks/project-queries";
import {
  canChoose,
  canSetUpElsewhere,
  isSettingUpAnywhere,
  preferredDaemonIds,
  projectPlacement,
  startBlockedReason,
} from "@/lib/projectPlacement";
import { describeCloneError } from "../Projects/cloneTargets";
import {
  choiceLeavesWorkspace,
  resolveChatMachine,
  startChoice,
  workspaceFilterMachine,
  workspaceMachine,
  workspacesOnMachine,
} from "@/lib/workspaceMachine";
import { daemonLabel } from "../Automations/daemonChoices";
import { DaemonWaitState } from "../DaemonWaitState";
import { NewChatProjectChip } from "./NewChatProjectChip";
import { MachinePicker } from "./MachinePicker";
import { useMakeDefaultMachine } from "@/hooks/useMakeDefaultMachine";
import { capabilities } from "@/services/controlPlane/capabilities";
import { ChatInput, type ComposerPrefill } from "./ChatInput";
import { ReliantIcon } from "../icons/ReliantIcon";
import { CreateWorktreeModal } from "../Worktrees/CreateWorktreeModal";
import { LoadMoreWorktreesRow } from "../Worktrees/LoadMoreWorktreesRow";
import { DiscoverWorktreesModal } from "../Worktrees/DiscoverWorktreesModal";
import {
  FolderGit2,
  ChevronDown,
  Check,
  Search,
  FolderPlus,
  CloudOff,
  Loader2,
} from "lucide-react";
import { ResumeDaemonPill } from "./ResumeDaemonPill";
import { OomKillBanner } from "./OomKillBanner";
import { DiskPressureBanner } from "./DiskPressureBanner";
import { toast } from "sonner";
import { cn } from "../../lib/utils";
import Tooltip from "../forge-ui/tooltip";
import { trackEvent } from "../../lib/analytics";
import { PendingFirstMessage } from "./PendingFirstMessage";
import { useCurrentWorktree, useCachedWorktrees, getCachedWorktree } from "../../hooks/worktree-queries";

// Text handed back after a failed start goes through the composer's prefill.
// Its ids count up from here, so they can never equal a host's prefill id
// (hosts count up from 0) and be skipped as one already applied.
const RESTORED_PREFILL_BASE_ID = 1_000_000;

interface NewChatViewProps {
  tabId: string;
  onNavigateToWorktrees?: () => void;
  isFocused?: boolean; // NEW: Whether this pane has focus
  onChatCreated?: (chatId: string) => void; // Optional: Callback when chat is created (for command center)
  /**
   * Shown below the workspace controls, by a host whose chat is about one
   * thing (the workflow editor) and has something specific to suggest. Told
   * whether the chat will run with no machine.
   */
  emptyState?: (context: { noMachine: boolean }) => ReactNode;
  composerPrefill?: ComposerPrefill;
  /**
   * The home host's additions to the welcome block, for a pane with no host
   * empty state: a heading under the mark, and a panel under the workspace
   * controls (the app shell puts the project's apps there — Apps/HomeNewChat).
   */
  heading?: ReactNode;
  welcomePanel?: ReactNode;
  /**
   * Show the project chip (the chat window's global New chat picks a project
   * and lets the user change it). Off for a host whose chat belongs to one
   * thing already, like the workflow editor's.
   */
  showProjectChip?: boolean;
}

export function NewChatView({
  tabId: _tabId,
  isFocused = true, // Default to focused
  onChatCreated,
  emptyState,
  composerPrefill,
  heading,
  welcomePanel,
  showProjectChip = false,
}: NewChatViewProps) {
  const [isCreating, setIsCreating] = useState(false);
  // The message being sent while StartChat is in flight (PendingFirstMessage).
  const [pendingFirst, setPendingFirst] = useState<{
    content: string;
    attachmentCount: number;
  } | null>(null);
  // A failed start hands its text back to the composer through `prefill`.
  const [restoredPrefill, setRestoredPrefill] = useState<ComposerPrefill | null>(null);
  // A new prefill from the host supersedes text handed back earlier.
  useEffect(() => {
    setRestoredPrefill(null);
  }, [composerPrefill?.id]);
  const [showAddMachine, setShowAddMachine] = useState(false);
  const [showCreateWorktreeModal, setShowCreateWorktreeModal] = useState(false);
  const [showDiscoverWorktreeModal, setShowDiscoverWorktreeModal] = useState(false);
  const [showWorkspaceDropdown, setShowWorkspaceDropdown] = useState(false);
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState<
    string | undefined
  >(undefined);
  const chatInputRef = useRef<HTMLTextAreaElement>(null);
  const workspaceDropdownRef = useRef<HTMLDivElement>(null);

  const currentWorktree = useCurrentWorktree();
  const currentProject = useProjectStore((state) => state.currentProject);
  // The workspaces the app already holds (main, the selection, whatever a
  // picker has paged in); opening the picker loads the list itself.
  const worktrees = useCachedWorktrees(currentProject?.id);
  const switchWorktreeContext = useWorktreeStore(
    (state) => state.switchWorktreeContext
  );
  const ensureApiKeyOrShowModal = useApiKeySetupStore(
    (state) => state.ensureApiKeyOrShowModal
  );
  const {
    activeDaemon,
    daemons,
    loading: daemonLoading,
    answered: daemonsAnswered,
    refresh: refreshDaemons,
  } = useDaemonStatus();
  const daemonConnected = Boolean(activeDaemon);

  // Where the chat runs (NO_MACHINE_CHATS.md §2.1). Null until the user picks:
  // the default is the user's machine whenever they have one, and No machine
  // only when they have none at all. A machine that is starting, asleep or
  // reconnecting still counts — the composer waits for it — and the desktop
  // app's own daemon gets the moments it needs to register before an empty
  // list means "none".
  const canPickMachine = useCapability("chatDaemonSelection");
  const [machineChoice, setMachineChoice] = useState<ChatMachineChoice | null>(null);
  const awaitingBundledDaemon = useBundledDaemonPending(hasMachine(daemons));
  // The machine a new chat lands on is the one the project is installed on
  // (research/CHAT_MACHINE_PLACEMENT.md), so the picker names that one.
  // While a clone onto one of them is landing, the rows are re-read so the
  // machine turns ready (and the composer opens) without a reload.
  const [installsPollMs, setInstallsPollMs] = useState<number | false>(false);
  const installs = useProjectDaemonInstalls(true, installsPollMs);
  const installRows = installs.data ?? [];
  // Which of the user's machines have this project (lib/projectPlacement.ts):
  // the picker offers those, and sets the project up on any other first.
  const placement = projectPlacement(installRows, currentProject?.id);
  const settingUpAnywhere = isSettingUpAnywhere(installRows, currentProject?.id);
  useEffect(() => {
    setInstallsPollMs(settingUpAnywhere ? 3_000 : false);
  }, [settingUpAnywhere]);
  const { makeDefault } = useMakeDefaultMachine();
  // Until the registry has answered, nothing is known about the user's
  // machines — including when the first read failed (a tab waking with a dead
  // connection). An empty list then is not "no machine", and No machine is a
  // one-way choice.
  const defaultMachine = defaultChatMachine({
    daemons,
    loading: !daemonsAnswered,
    awaitingBundledDaemon,
  });
  // A different project is a different set of checkouts: its new chat starts
  // from the default machine again, unless the user picked a machine first
  // and the new project is on it (or being set up there) — the project
  // picker offers exactly those for a picked machine (NewChatProjectChip).
  const [machineChoiceProjectId, setMachineChoiceProjectId] = useState(currentProject?.id);
  if (machineChoiceProjectId !== currentProject?.id) {
    setMachineChoiceProjectId(currentProject?.id);
    const keep =
      machineChoice !== null &&
      machineChoice !== DEFAULT_MACHINE &&
      machineChoice !== NO_MACHINE &&
      placement.known &&
      canChoose(placement, machineChoice);
    if (!keep) setMachineChoice(null);
  }
  // A branch workspace is a checkout on one machine, so selecting one selects
  // its machine (lib/workspaceMachine.ts); the server refuses anything else.
  const machineIds = new Set(daemons.map((d) => d.daemonId));
  const selectedWorkspace = selectedWorkspaceId
    ? (worktrees.find((w) => w.id === selectedWorkspaceId) ?? getCachedWorktree(selectedWorkspaceId))
    : undefined;
  const effectiveChoice = canPickMachine ? machineChoice : null;
  const resolvedMachine = resolveChatMachine({
    choice: effectiveChoice,
    fallback: defaultMachine ?? DEFAULT_MACHINE,
    workspaceDaemonId: workspaceMachine(selectedWorkspace, machineIds),
  });
  const machine = resolvedMachine.value;
  const noMachine = machine === NO_MACHINE;
  // The machine the workspace list is narrowed to, and that a workspace made
  // or adopted from here goes on; undefined leaves both to the server.
  const workspaceMachineId = workspaceFilterMachine(effectiveChoice, resolvedMachine);
  // A chat on a machine still waits for one to be connected, as before.
  const machineReady = noMachine || daemonConnected;

  // The machine "default" names: where the project is (or is landing), by
  // the server's own default order (lib/chatMachine defaultMachineDaemon).
  const defaultDaemon = defaultMachineDaemon(daemons, preferredDaemonIds(installRows, currentProject?.id));
  const machineName = (daemonId: string) =>
    daemonLabel(daemons.find((d) => d.daemonId === daemonId), daemonId);
  // The machine the chat will run on, when it is one: the pick, else what the
  // default resolves to. A workspace's machine has the project by definition.
  const runsOnDaemonId = noMachine
    ? undefined
    : machine === DEFAULT_MACHINE
      ? defaultDaemon?.daemonId
      : machine;
  // Never a send that fails: a machine the project is not set up on (or is
  // still landing on) holds the composer, and says what to do.
  const placementBlock =
    canPickMachine && currentProject && runsOnDaemonId && !resolvedMachine.lockedBy
      ? startBlockedReason({
          placement,
          daemonId: runsOnDaemonId,
          projectName: currentProject.name,
          machineName: machineName(runsOnDaemonId),
        })
      : null;
  const placementSettingUp = Boolean(runsOnDaemonId) && placement.stateOn(runsOnDaemonId ?? "") === "setting_up";

  // Set the project up on a machine that lacks it: the same clone "Clone it
  // onto <machine>" runs (CreateProjectFromRepo), into the project's path. A
  // plain folder has nothing to clone from, so it is only explained.
  const setUpProjectOn =
    currentProject && canSetUpElsewhere(currentProject)
      ? async (daemonId: string) => {
          await createProjectFromRepo({
            cloneUrl: currentProject.remote_url ?? "",
            daemonId,
            name: currentProject.name,
            path: currentProject.path,
          });
        }
      : undefined;

  const daemonWait = useDaemonWait({
    waiting: !machineReady && !daemonLoading,
    onRetry: refreshDaemons,
  });

  // Find the main worktree for this project
  const mainWorktree = worktrees.find((w) => w.is_main === true);

  // Filter out the main worktree and archived ones from the dropdown list, and
  // the workspaces on a machine other than the chosen one.
  const nonMainWorktrees = workspacesOnMachine(
    worktrees.filter((w) => !w.is_main && !w.deleted_at),
    workspaceMachineId,
  );
  // With several machines and none chosen, each workspace says where it is.
  const showWorkspaceMachines = daemons.length > 1 && !workspaceMachineId;

  // Auto-focus input when new chat view is shown
  useEffect(() => {
    if (isFocused) {
      const timer = setTimeout(() => {
        chatInputRef.current?.focus();
      }, 100);
      return () => clearTimeout(timer);
    }
  }, [isFocused]);

  // NOTE: We intentionally do NOT clear tempNewChatParams on mount.
  // ChatInput's workflow-change effect handles clearing when the workflow changes.
  // Clearing here would wipe user-set params (e.g. model) when NewChatView
  // remounts between chat creation attempts.

  // Check for API key and show setup modal if not configured
  useEffect(() => {
    ensureApiKeyOrShowModal();
  }, [ensureApiKeyOrShowModal]);

  // Close workspace dropdown when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (
        workspaceDropdownRef.current &&
        !workspaceDropdownRef.current.contains(event.target as Node)
      ) {
        setShowWorkspaceDropdown(false);
      }
    };
    if (showWorkspaceDropdown) {
      document.addEventListener("mousedown", handleClickOutside);
      return () =>
        document.removeEventListener("mousedown", handleClickOutside);
    }
  }, [showWorkspaceDropdown]);

  // Sync selected workspace with currentWorktree from the store
  useEffect(() => {
    const activeWorktrees = worktrees.filter((w) => !w.deleted_at);
    const selectedStillValid =
      selectedWorkspaceId &&
      activeWorktrees.some((w) => w.id === selectedWorkspaceId);

    if (selectedWorkspaceId && !selectedStillValid) {
      const newSelection =
        currentWorktree?.id || mainWorktree?.id || activeWorktrees[0]?.id;
      logger.info(
        "[NewChatView] Selected workspace no longer valid, resetting",
        {
          previousId: selectedWorkspaceId,
          newId: newSelection,
          reason: "workspace_archived_or_deleted",
        }
      );
      setSelectedWorkspaceId(newSelection);
      return;
    }

    // Sync with global currentWorktree when it changes
    const bestWorkspace =
      currentWorktree?.id || mainWorktree?.id || activeWorktrees[0]?.id;

    if (!selectedWorkspaceId || (currentWorktree?.id && selectedWorkspaceId !== currentWorktree.id)) {
      if (bestWorkspace) {
        setSelectedWorkspaceId(bestWorkspace);
      }
    }
  }, [currentWorktree?.id, mainWorktree?.id, worktrees, selectedWorkspaceId]);

  const handleCreateAndSend = async (
    content: string,
    attachmentIds?: string[],
    workflow?: string | null,
    workflowParams?: Record<string, unknown>
  ) => {
    if (!machineReady) {
      toast.error("No machine connected", {
        description: "Start a machine to begin chatting, or continue without a machine.",
      });
      return;
    }
    if (placementBlock) {
      toast.error("Not set up on that machine yet", { description: placementBlock });
      return;
    }
    if ((!content.trim() && !attachmentIds?.length) || isCreating) return;

    setIsCreating(true);
    // The composer has already cleared; until the chat exists, this is the
    // only place the message is on screen.
    setPendingFirst({ content, attachmentCount: attachmentIds?.length ?? 0 });
    try {
      // A chat with no machine runs in the project's main checkout: a branch
      // workspace is a checkout on one machine.
      const chatWorktreeId = noMachine ? mainWorktree?.id : selectedWorkspaceId || mainWorktree?.id;
      const { daemon_id: daemonId, no_machine: startNoMachine } = startOptionsForMachine(startChoice(resolvedMachine));

      if (!chatWorktreeId) {
        const error = `Cannot create chat: No worktree found. worktrees.length=${worktrees.length}, mainWorktree=${mainWorktree?.id}`;
        console.error(error);
        throw new Error(error);
      }

      const chat = await useChatStore
        .getState()
        .startChat(chatWorktreeId, content, attachmentIds, workflowParams, workflow, undefined, {
          daemonId,
          noMachine: startNoMachine,
        });

      trackEvent('chat_created', {
        has_attachments: Boolean(attachmentIds?.length),
        workflow: workflow ?? 'default',
        no_machine: noMachine,
      });

      useChatParamsStore.getState().transferTempToChat(chat.id);
      useChatStore.getState().selectChat(chat);

      if (onChatCreated) {
        onChatCreated(chat.id);
      }

      const { clearAttachments } = useAttachmentStore.getState();
      clearAttachments("temp");

      const projectId = useProjectStore.getState().currentProject?.id;
      if (projectId) {
        useWorkspaceStateStore.getState().clearNewChatDraft(projectId);
      }
    } catch (error) {
      console.error("Failed to create chat:", error);
      const errorMessage = error instanceof Error 
        ? error.message 
        : "An unexpected error occurred";
      // Hand the text back: the composer cleared it on send, and a chat that
      // was never created has no transcript to recover it from. Attachments
      // cannot be handed back from here (the composer released them), so say so.
      setRestoredPrefill((previous) => ({
        text: content,
        id: (previous?.id ?? RESTORED_PREFILL_BASE_ID) + 1,
      }));
      toast.error("Couldn't start the chat", {
        description: attachmentIds?.length
          ? `${errorMessage} Your message is back in the box; attach your files again before sending.`
          : `${errorMessage} Your message is back in the box.`,
      });
      throw error;
    } finally {
      setIsCreating(false);
      setPendingFirst(null);
    }
  };

  // Listen for checklist-triggered worktree creation
  useEffect(() => {
    const handler = () => setShowCreateWorktreeModal(true);
    window.addEventListener("open-create-worktree-modal", handler);
    return () => window.removeEventListener("open-create-worktree-modal", handler);
  }, []);

  const handleWorktreeCreated = async (worktreeId: string) => {
    setShowWorkspaceDropdown(false);
    // Switch the global context too. Setting local state alone loses the
    // selection: the sync effect below sees an id that isn't the current
    // worktree and snaps the toggle back.
    await handleWorkspaceSelect(worktreeId);
  };

  // importWorktree refreshes the workspace lists itself.
  const handleWorktreesImported = (_importedWorktreeIds?: string[]) => {};

  const handleWorkspaceSelect = async (worktreeId: string | null) => {
    // Read from the cache rather than the render closure: a workspace created
    // moments ago is already cached but not yet in this render's list.
    if (worktreeId && currentProject) {
      const worktree = getCachedWorktree(worktreeId);
      if (worktree) {
        await switchWorktreeContext(currentProject.id, worktree);
        setSelectedWorkspaceId(worktreeId);
      }
    } else if (currentProject && mainWorktree) {
      await switchWorktreeContext(currentProject.id, mainWorktree);
      setSelectedWorkspaceId(mainWorktree.id);
    }
    setShowWorkspaceDropdown(false);
  };

  // Choosing a machine that does not hold the selected workspace falls back to
  // the main checkout, which every machine has.
  const handleMachineChange = (next: ChatMachineChoice) => {
    setMachineChoice(next);
    if (choiceLeavesWorkspace(next, resolvedMachine.lockedBy)) {
      void handleWorkspaceSelect(mainWorktree?.id ?? null);
    }
  };

  // Get display name for selected workspace
  const selectedWorkspaceName = selectedWorkspaceId
    ? worktrees.find((w) => w.id === selectedWorkspaceId)?.branch ||
      worktrees.find((w) => w.id === selectedWorkspaceId)?.name ||
      (currentWorktree?.id === selectedWorkspaceId
        ? currentWorktree.branch || currentWorktree.name
        : null) ||
      "Select workspace"
    : currentProject?.default_branch || "main";

  return (
    <div className="flex flex-col h-full min-h-0 bg-background">
      {/* Welcome Content */}
      <div className={cn("relative flex-1 min-h-0 overflow-y-auto", emptyState ? "px-4" : "px-8")}>
        <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_50%_38%,hsl(var(--muted)_/_0.16),transparent_62%)]" />

        {/* While the chat is being created, the message the user just sent
            takes the welcome block's place, sitting where the transcript will
            put it, above the composer. */}
        {pendingFirst ? (
          <div className="relative z-10 mx-auto flex min-h-full w-full max-w-5xl flex-col justify-end py-6">
            <PendingFirstMessage
              content={pendingFirst.content}
              attachmentCount={pendingFirst.attachmentCount}
            />
          </div>
        ) : (
        /* Without a host empty state the welcome block is all there is, so
            it centres in the space above the composer instead of pinning to
            the top of an otherwise empty pane. */
        <div
          className={cn(
            "relative z-10 min-h-full w-full max-w-5xl mx-auto",
            emptyState ? "grid grid-rows-[auto_1fr]" : "flex flex-col justify-center",
          )}
        >
          <div className={cn("flex items-center justify-center", emptyState ? "pt-4" : "py-8")}>
            <div className="w-full max-w-xl mx-auto flex flex-col items-center text-center gap-3">
              <ResumeDaemonPill placement="inline" />

              {/* A host with its own empty state is a panel with its own
                  header, and needs the room. */}
              {!emptyState && (
                <div className="inline-flex h-10 w-10 items-center justify-center">
                  <ReliantIcon className="h-10 w-10" />
                </div>
              )}
              {!emptyState && heading}

              {/* Workspace controls */}
              <div
                className="w-full flex flex-wrap items-center justify-center gap-3"
                data-onboarding="workspace-buttons"
              >
                {/* The project the chat starts in: the global New chat picks
                    it (filter, open chat, last used); change it here. */}
                {showProjectChip && (
                  <NewChatProjectChip
                    disabled={isCreating}
                    // A machine the user picked themselves: the projects on
                    // it come first, the rest are set up there on the way.
                    machine={
                      canPickMachine && effectiveChoice && effectiveChoice !== NO_MACHINE
                        ? { daemonId: effectiveChoice, name: machineName(effectiveChoice) }
                        : undefined
                    }
                    installs={installRows}
                  />
                )}

                {canPickMachine && (
                  <MachinePicker
                    value={machine}
                    onChange={handleMachineChange}
                    daemons={daemons}
                    defaultDaemon={defaultDaemon}
                    onMakeDefault={makeDefault}
                    project={
                      currentProject
                        ? {
                            name: currentProject.name,
                            placement,
                            onSetUp: setUpProjectOn,
                            describeError: describeCloneError,
                          }
                        : undefined
                    }
                    disabled={isCreating}
                  />
                )}

                {/* Workspaces are checkouts on a machine; a chat with no
                    machine has none to pick. */}
                {!noMachine && (
                <>
                {/* Workspace Selector */}
                <div className="relative" ref={workspaceDropdownRef}>
                  <Tooltip
                    content="Workspaces are isolated copies of the project, powered by git worktrees — run several agents in parallel without conflicts."
                    side="top"
                  >
                    <button
                      onClick={() => setShowWorkspaceDropdown(!showWorkspaceDropdown)}
                      className="flex items-center gap-2 px-5 py-2.5 text-sm font-medium text-foreground bg-muted/40 border border-border/70 rounded-lg hover:border-border hover:bg-muted/60 transition-colors"
                    >
                      <FolderGit2 className="w-4 h-4" />
                      <span className="max-w-[180px] truncate">{selectedWorkspaceName}</span>
                      <ChevronDown className="w-3.5 h-3.5 opacity-60" />
                    </button>
                  </Tooltip>

                  {showWorkspaceDropdown && (
                    <div className="absolute top-full left-0 mt-1 border border-border/50 rounded-md elevation-4 z-[1000] min-w-60 bg-[var(--chat-dropdown-bg)] overflow-hidden">
                      <div className="overflow-y-auto max-h-60">
                        {/* Main workspace */}
                        <button
                          onClick={() => handleWorkspaceSelect(mainWorktree?.id ?? null)}
                          className={cn(
                            "w-full px-3 py-2 text-left text-xs transition-colors border-b hover:bg-muted",
                            selectedWorkspaceId === mainWorktree?.id && "bg-muted"
                          )}
                        >
                          <div className="flex items-center justify-between gap-2">
                            <div>
                              <div className="font-medium">
                                {mainWorktree?.branch || currentProject?.default_branch || "main"}
                              </div>
                              <div className="text-xs opacity-60">Main workspace</div>
                            </div>
                            {selectedWorkspaceId === mainWorktree?.id && (
                              <Check className="w-3 h-3 text-primary flex-shrink-0" />
                            )}
                          </div>
                        </button>

                        {/* Other workspaces */}
                        {nonMainWorktrees.map((worktree) => (
                          <button
                            key={worktree.id}
                            onClick={() => handleWorkspaceSelect(worktree.id)}
                            className={cn(
                              "w-full px-3 py-2 text-left text-xs transition-colors border-b hover:bg-muted",
                              selectedWorkspaceId === worktree.id && "bg-muted"
                            )}
                          >
                            <div className="flex items-center justify-between gap-2">
                              <div className="min-w-0">
                                <div className="font-medium truncate">{worktree.branch || worktree.name}</div>
                                {showWorkspaceMachines && worktree.daemon_id && (
                                  <div className="text-xs text-muted-foreground truncate">
                                    On {machineName(worktree.daemon_id)}
                                  </div>
                                )}
                              </div>
                              {selectedWorkspaceId === worktree.id && (
                                <Check className="w-3 h-3 text-primary flex-shrink-0" />
                              )}
                            </div>
                          </button>
                        ))}

                        {nonMainWorktrees.length === 0 && (
                          <div className="px-3 py-2 text-xs text-muted-foreground border-b">
                            {workspaceMachineId && daemons.length > 1
                              ? `No other workspaces on ${machineName(workspaceMachineId)}`
                              : "No additional workspaces"}
                          </div>
                        )}
                        <LoadMoreWorktreesRow projectId={currentProject?.id} />
                      </div>

                      <div className="sticky bottom-0 bg-[var(--chat-dropdown-bg)] border-t border-border/50">
                        {/* Discover workspaces */}
                        <button
                          onClick={() => {
                            setShowDiscoverWorktreeModal(true);
                            setShowWorkspaceDropdown(false);
                          }}
                          className="w-full px-3 py-2 text-xs text-center border-b"
                        >
                          <div className="flex items-center justify-center gap-2 text-primary">
                            <Search className="w-3 h-3" />
                            <span className="font-medium">Discover workspace</span>
                          </div>
                        </button>

                        {/* Create new */}
                        <button
                          onClick={() => {
                            setShowCreateWorktreeModal(true);
                            setShowWorkspaceDropdown(false);
                          }}
                          className="w-full px-3 py-2 text-xs text-center"
                        >
                          <div className="flex items-center justify-center gap-2 text-primary">
                            <FolderPlus className="w-3.5 h-3.5 shrink-0 -translate-y-px" />
                            <span className="font-medium leading-none">New workspace</span>
                          </div>
                        </button>
                      </div>
                    </div>
                  )}
                </div>

                {/* Quick create workspace */}
                <Tooltip
                  content="Create an isolated copy of your codebase (a git worktree) so another agent can work in parallel without conflicts."
                  side="top"
                >
                  <button
                    onClick={() => setShowCreateWorktreeModal(true)}
                    className="flex items-center gap-2 px-5 py-2.5 text-sm font-medium text-foreground bg-muted/40 border border-border/70 rounded-lg hover:border-border hover:bg-muted/60 transition-colors"
                  >
                    <FolderPlus className="w-4 h-4 shrink-0" />
                    <span className="leading-none">New workspace</span>
                  </button>
                </Tooltip>
                </>
                )}
              </div>

              {/* Why the machine moved with the workspace. */}
              {canPickMachine && resolvedMachine.lockedBy && daemons.length > 1 && (
                <p role="note" data-testid="new-chat-workspace-machine-note" className="text-xs text-muted-foreground">
                  Runs on {machineName(resolvedMachine.lockedBy)}, where this workspace lives.
                </p>
              )}

              {/* Why the composer is holding: the project is not on the
                  chosen machine yet. The picker has the action. */}
              {placementBlock && (
                <p
                  role="status"
                  data-testid="new-chat-placement-note"
                  className="flex items-center gap-1.5 text-xs text-muted-foreground"
                >
                  {placementSettingUp && (
                    <Loader2 className="h-3 w-3 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                  )}
                  {placementBlock}
                </p>
              )}

              {!emptyState && welcomePanel && <div className="w-full pt-3">{welcomePanel}</div>}
            </div>
          </div>

          {emptyState && (
            <div className="w-full py-3">{emptyState({ noMachine })}</div>
          )}

        </div>
        )}
      </div>

      {/* OOM banner — machine ran out of memory recently (cloud daemons) */}
      <OomKillBanner />
      <DiskPressureBanner />

      {/* Message Input - Collapsible when not focused */}
      {/* A machine that is BOOTING is not the same as one that was never
          started, but this banner used to say "No machine connected" for both
          — telling a user to go start a machine that was 15 seconds from
          ready. The shared wait state distinguishes them. */}
      {!machineReady && !daemonLoading && daemonWait.state && (
        <>
          <DaemonWaitState
            state={daemonWait.state}
            variant="inline"
            onRetry={
              capabilities.cloudDaemons
                ? daemonWait.retryNow
                : () => setShowAddMachine(true)
            }
          />
          {/* The machine is unavailable; the chat does not have to wait for it. */}
          {canPickMachine && (
            <div className="flex-shrink-0 px-4 pb-1 text-center text-xs text-muted-foreground">
              <button
                type="button"
                onClick={() => setMachineChoice(NO_MACHINE)}
                data-testid="new-chat-continue-without-machine"
                className="font-medium text-primary hover:underline"
              >
                Continue without machine
              </button>{" "}
              — web &amp; integrations only
            </div>
          )}
        </>
      )}
      {noMachine && (
        <p
          role="note"
          data-testid="new-chat-no-machine-hint"
          className="flex flex-shrink-0 items-center justify-center gap-1.5 px-4 pb-1.5 text-xs text-muted-foreground"
        >
          <CloudOff className="h-3 w-3" aria-hidden="true" />
          {NO_MACHINE_COMPOSER_HINT}
        </p>
      )}
      {isFocused ? (
        <div className="flex-shrink-0">
          <ChatInput
            ref={chatInputRef}
            onSend={handleCreateAndSend}
            disabled={isCreating || !machineReady || Boolean(placementBlock)}
            worktreeId={selectedWorkspaceId || mainWorktree?.id}
            prefill={restoredPrefill ?? composerPrefill}
          />
        </div>
      ) : (
        <div className="p-2 border-t border-border bg-muted/20 text-center text-sm text-muted-foreground flex-shrink-0">
          Click to focus and start a new chat
        </div>
      )}

      {/* Workspace Modals */}
      {currentProject && (
        <>
          <CreateWorktreeModal
            isOpen={showCreateWorktreeModal}
            onClose={() => setShowCreateWorktreeModal(false)}
            onWorktreeCreated={handleWorktreeCreated}
            projectId={currentProject.id}
            daemonId={workspaceMachineId}
          />
          <DiscoverWorktreesModal
            isOpen={showDiscoverWorktreeModal}
            onClose={() => setShowDiscoverWorktreeModal(false)}
            onWorktreesImported={handleWorktreesImported}
            projectId={currentProject.id}
            daemonId={workspaceMachineId}
          />
        </>
      )}

      {showAddMachine && (
        <AddMachineSheet open onClose={() => setShowAddMachine(false)} onReady={() => refreshDaemons()} />
      )}
    </div>
  );
}