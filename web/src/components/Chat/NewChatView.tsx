import { logger } from "../../lib/logger";
import { useState, useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { ConnectDaemonModal } from "../Layout/ConnectDaemonModal";
import { useChatStore } from "../../store/chatStore"; // For getState() and setState() only — also subscribed via selector below
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatList } from "../../hooks/chat-queries";
import { useAttachmentStore } from "../../store/attachmentStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { useChatParamsStore } from "../../store/chatParamsStore";
import { useNavigate } from "@tanstack/react-router";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { capabilities } from "@/services/controlPlane/capabilities";
import { ChatInput } from "./ChatInput";
import { ReliantIcon } from "../icons/ReliantIcon";
import { WorkflowStarterCards } from "../Onboarding/WorkflowStarterCards";
import { CreateWorktreeModal } from "../Worktrees/CreateWorktreeModal";
import { DiscoverWorktreesModal } from "../Worktrees/DiscoverWorktreesModal";
import {
  FolderGit2,
  ChevronDown,
  Check,
  Search,
  FolderPlus,
  Activity,
} from "lucide-react";
import { ResumeDaemonPill } from "./ResumeDaemonPill";
import { OomKillBanner } from "./OomKillBanner";
import { toast } from "sonner";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import { trackEvent } from "../../lib/analytics";

interface NewChatViewProps {
  tabId: string;
  onNavigateToWorktrees?: () => void;
  isFocused?: boolean; // NEW: Whether this pane has focus
  onChatCreated?: (chatId: string) => void; // Optional: Callback when chat is created (for command center)
}

export function NewChatView({
  tabId: _tabId,
  isFocused = true, // Default to focused
  onChatCreated,
}: NewChatViewProps) {
  const [isCreating, setIsCreating] = useState(false);
  const [showConnectDaemonModal, setShowConnectDaemonModal] = useState(false);
  const [showCreateWorktreeModal, setShowCreateWorktreeModal] = useState(false);
  const [showDiscoverWorktreeModal, setShowDiscoverWorktreeModal] = useState(false);
  const [showWorkspaceDropdown, setShowWorkspaceDropdown] = useState(false);
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState<
    string | undefined
  >(undefined);
  const chatInputRef = useRef<HTMLDivElement>(null);
  const workspaceDropdownRef = useRef<HTMLDivElement>(null);
  const navigate = useNavigate();

  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  // Starter cards stay available on every new-chat view — not just first run —
  // so users can re-pick a workflow (landing page, pitch deck, blog, …) at any
  // time. Previously they were gated to `chats.size === 0` and vanished after
  // the first chat, which made those flows unreachable. We still wait for the
  // initial `loadChats` to complete (`hasLoaded`, set on both success and
  // failure) so we don't flash the cards while chats are still loading.
  //
  // The blocking full-screen picker (`lockChatInput`) is still reserved for the
  // genuine first-run empty state: a project with zero chats where the user
  // hasn't picked a starter yet. Returning users get the cards inline, never a
  // modal. The post-tour modal variant is mounted separately in ModernApp and
  // is not affected by this gate.
  const chatsListProjectId = useProjectStore((state) => state.currentProject?.id);
  const { data: chatsList, isSuccess: chatsQuerySucceeded } =
    useChatList(chatsListProjectId);
  const chatsCount = chatsList?.length ?? 0;
  // hasLoaded (Zustand) still gates the first-run experience; combine with the
  // list query having resolved so we never flash the empty state pre-fetch.
  const chatsLoaded =
    useChatStore((state) => state.hasLoaded) && chatsQuerySucceeded;
  const hasNoChatsInProject = chatsLoaded && chatsCount === 0;
  const hasPickedStarter = useChatParamsStore((s) => Boolean(s.tempNewChatWorkflow));
  const lockChatInput = hasNoChatsInProject && !hasPickedStarter;
  // Show inline cards whenever we aren't showing the blocking first-run modal.
  const showInlineCards = chatsLoaded && !lockChatInput;
  const switchWorktreeContext = useWorktreeStore(
    (state) => state.switchWorktreeContext
  );
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const currentProject = useProjectStore((state) => state.currentProject);
  const ensureApiKeyOrShowModal = useApiKeySetupStore(
    (state) => state.ensureApiKeyOrShowModal
  );
  const { activeDaemon, loading: daemonLoading } = useDaemonStatus();
  const daemonConnected = Boolean(activeDaemon);

  // Find the main worktree for this project
  const mainWorktree = worktrees.find((w) => w.is_main === true);

  // Filter out the main worktree and archived ones from the dropdown list
  const nonMainWorktrees = worktrees.filter((w) => !w.is_main && !w.deleted_at);

  // Auto-focus input when new chat view is shown
  useEffect(() => {
    if (isFocused) {
      const timer = setTimeout(() => {
        chatInputRef.current?.focus();
      }, 100);
      return () => clearTimeout(timer);
    }
  }, [isFocused]);

  // Lock body scroll while the starter-picker modal is open.
  useEffect(() => {
    if (!lockChatInput) return;
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = prev;
    };
  }, [lockChatInput]);

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
    if (!daemonConnected) {
      toast.error("No machine connected", {
        description: "Start a machine to begin chatting.",
      });
      return;
    }
    if ((!content.trim() && !attachmentIds?.length) || isCreating) return;

    setIsCreating(true);
    try {
      const chatWorktreeId = selectedWorkspaceId || mainWorktree?.id;

      if (!chatWorktreeId) {
        const error = `Cannot create chat: No worktree found. worktrees.length=${worktrees.length}, mainWorktree=${mainWorktree?.id}`;
        console.error(error);
        throw new Error(error);
      }

      const chat = await useChatStore
        .getState()
        .createChat(chatWorktreeId, content, attachmentIds, workflowParams, workflow);

      trackEvent('chat_created', {
        has_attachments: Boolean(attachmentIds?.length),
        workflow: workflow ?? 'default',
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
      toast.error("Failed to create chat", {
        description: errorMessage,
      });
      throw error;
    } finally {
      setIsCreating(false);
    }
  };

  // Listen for checklist-triggered worktree creation
  useEffect(() => {
    const handler = () => setShowCreateWorktreeModal(true);
    window.addEventListener("open-create-worktree-modal", handler);
    return () => window.removeEventListener("open-create-worktree-modal", handler);
  }, []);

  const handleWorktreeCreated = (worktreeId: string) => {
    setSelectedWorkspaceId(worktreeId);
    setShowWorkspaceDropdown(false);
  };

  const handleWorktreesImported = (_importedWorktreeIds?: string[]) => {
    if (currentProject) {
      loadWorktrees(currentProject.id);
    }
  };

  const handleWorkspaceSelect = async (worktreeId: string | null) => {
    if (worktreeId && currentProject) {
      const worktree = worktrees.find((w) => w.id === worktreeId);
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

  // Get display name for selected workspace
  const selectedWorkspaceName = selectedWorkspaceId
    ? worktrees.find((w) => w.id === selectedWorkspaceId)?.branch ||
      worktrees.find((w) => w.id === selectedWorkspaceId)?.name ||
      (currentWorktree?.id === selectedWorkspaceId
        ? currentWorktree.branch || currentWorktree.name
        : null) ||
      "Select Workspace"
    : currentProject?.default_branch || "main";

  return (
    <div className="flex flex-col h-full min-h-0 bg-background">
      {/* Welcome Content */}
      <div className="relative flex-1 min-h-0 px-8 overflow-y-auto">
        <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_50%_38%,hsl(var(--muted)_/_0.16),transparent_62%)]" />

        <div className="relative z-10 min-h-full w-full max-w-5xl mx-auto grid grid-rows-[auto_1fr_auto]">
          <div className="flex items-center justify-center pt-8">
            <div className="w-full max-w-xl mx-auto flex flex-col items-center text-center gap-3">
              <ResumeDaemonPill placement="inline" />

              <div className="inline-flex h-10 w-10 items-center justify-center">
                <ReliantIcon className="h-10 w-10" />
              </div>

              {/* Workspace controls */}
              <div
                className="w-full flex flex-wrap items-center justify-center gap-3"
                data-onboarding="workspace-buttons"
              >
                {/* Workspace Selector */}
                <div className="relative" ref={workspaceDropdownRef}>
                  <Tooltip
                    content="Workspaces are isolated copies of your codebase, powered by git worktrees — run multiple agents in parallel without conflicts."
                    placement="top"
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
                              <div className="text-[11px] opacity-60">Main workspace</div>
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
                              <span className="font-medium">{worktree.branch || worktree.name}</span>
                              {selectedWorkspaceId === worktree.id && (
                                <Check className="w-3 h-3 text-primary flex-shrink-0" />
                              )}
                            </div>
                          </button>
                        ))}

                        {nonMainWorktrees.length === 0 && (
                          <div className="px-3 py-2 text-xs text-muted-foreground border-b">
                            No additional workspaces
                          </div>
                        )}
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
                  placement="top"
                >
                  <button
                    onClick={() => setShowCreateWorktreeModal(true)}
                    className="flex items-center gap-2 px-5 py-2.5 text-sm font-medium text-foreground bg-muted/40 border border-border/70 rounded-lg hover:border-border hover:bg-muted/60 transition-colors"
                  >
                    <FolderPlus className="w-4 h-4 shrink-0" />
                    <span className="leading-none">New workspace</span>
                  </button>
                </Tooltip>
              </div>

            </div>
          </div>

          {/* Starter cards — pick a workflow to seed the next chat. Shown on
              every new-chat view (except when the blocking first-run modal is
              up) so landing-page / pitch-deck / blog stay reachable, not just
              on the first-ever chat. */}
          {showInlineCards && (
            <div className="w-full px-4 py-4 md:py-5">
              <WorkflowStarterCards />
            </div>
          )}

        </div>
      </div>

      {/* OOM banner — machine ran out of memory recently (cloud daemons) */}
      <OomKillBanner />

      {/* Message Input - Collapsible when not focused */}
      {!daemonConnected && !daemonLoading && (() => {
        // In cloud mode (control-plane deployment) route to the in-app
        // Machines settings section; otherwise fall back to the local
        // "connect a daemon" modal.
        const isCloud = capabilities.cloudDaemons;
        return (
          <div className="flex items-center justify-center gap-2 border-t border-yellow-500/20 bg-yellow-500/5 px-4 py-2.5 text-sm text-yellow-600 dark:text-yellow-400">
            <Activity className="h-4 w-4" />
            <span>
              No machine connected.{" "}
              {isCloud ? (
                <button
                  type="button"
                  onClick={() =>
                    navigate({
                      to: "/settings/$section",
                      params: { section: "environments" },
                    })
                  }
                  className="font-medium underline underline-offset-2 hover:text-yellow-700 dark:hover:text-yellow-300"
                >
                  Manage machines
                </button>
              ) : (
                <button
                  type="button"
                  onClick={() => setShowConnectDaemonModal(true)}
                  className="font-medium underline underline-offset-2 hover:text-yellow-700 dark:hover:text-yellow-300"
                >
                  Start a cloud machine or run one locally
                </button>
              )}{" "}
              to begin chatting.
            </span>
          </div>
        );
      })()}
      {isFocused ? (
        <div className="flex-shrink-0">
          <ChatInput
            ref={chatInputRef}
            onSend={handleCreateAndSend}
            disabled={isCreating || !daemonConnected}
            worktreeId={selectedWorkspaceId || mainWorktree?.id}
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
          />
          <DiscoverWorktreesModal
            isOpen={showDiscoverWorktreeModal}
            onClose={() => setShowDiscoverWorktreeModal(false)}
            onWorktreesImported={handleWorktreesImported}
            projectId={currentProject.id}
          />
        </>
      )}

      <ConnectDaemonModal
        isOpen={showConnectDaemonModal}
        onClose={() => setShowConnectDaemonModal(false)}
      />

      {lockChatInput &&
        createPortal(
          <div
            className="fixed inset-0 z-50 flex items-center justify-center p-4 sm:p-6"
            role="dialog"
            aria-modal="true"
            aria-labelledby="starter-picker-title"
          >
            <div
              className="absolute inset-0 bg-black/80 backdrop-blur-xl"
              aria-hidden="true"
            />
            <div className="relative w-full max-w-5xl max-h-[calc(100vh-80px)] overflow-y-auto rounded-2xl border border-white/10 bg-[hsl(var(--surface-modal))] px-6 py-8 sm:px-10 sm:py-10 elevation-5 animate-in fade-in-0 zoom-in-95 duration-300">
              <h2 id="starter-picker-title" className="sr-only">
                Pick a starting point
              </h2>
              <WorkflowStarterCards />
            </div>
          </div>,
          document.body,
        )}
    </div>
  );
}