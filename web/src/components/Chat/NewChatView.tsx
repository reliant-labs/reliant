import { logger } from "../../lib/logger";
import { useState, useCallback, useEffect, useRef } from "react";
import { useChatStore } from "../../store/chatStore"; // For getState() and setState() only
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useAttachmentStore } from "../../store/attachmentStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { useChatParamsStore } from "../../store/chatParamsStore";
import { ChatInput } from "./ChatInput";
import { ReliantIcon } from "../icons/ReliantIcon";
import { CreateWorktreeModal } from "../Worktrees/CreateWorktreeModal";
import { DiscoverWorktreesModal } from "../Worktrees/DiscoverWorktreesModal";
import { getFileTree } from "../../api/fileSystem";
import {
  FolderGit2,
  ChevronDown,
  Check,
  Search,
  Compass,
  Bug,
  Wrench,
  Code2,
  ArrowRightLeft,
} from "lucide-react";
import { LuFolderPlus } from "react-icons/lu";
import { toast } from "sonner";
import { cn } from "../../lib/utils";
import { safeGetSetting, upsertStringSetting } from "../../lib/settingsPersistence";

interface NewChatViewProps {
  tabId: string;
  onNavigateToWorktrees?: () => void;
  isFocused?: boolean; // NEW: Whether this pane has focus
  onChatCreated?: (chatId: string) => void; // Optional: Callback when chat is created (for command center)
}

const MIGRATION_COMPLETED_SETTING_KEY = "migration.completed";
const migrationDetectionTargets = [
  ".claude",
  ".cursor",
  ".codex",
  ".windsurf",
  "CLAUDE.md",
  "AGENTS.md",
  ".cursorrules",
  ".windsurfrules",
  ".mcp.json",
];

const starterPrompts = [
  {
    label: "Build",
    hint: "Build something new",
    icon: Wrench,
    prompt: `Help me implement a new feature end-to-end.

Please:
1) clarify requirements and edge cases
2) propose a technical approach aligned with existing patterns
3) break implementation into ordered tasks
4) list files to change and why
5) include testing/validation steps and rollout notes

Keep it practical and production-oriented.`,
  },
  {
    label: "Explain",
    hint: "Map this codebase",
    icon: Compass,
    prompt: `Explain this codebase.

Please cover:
1) overall architecture and data flow
2) key folders/files and what they’re responsible for
3) how a user request moves through the app (frontend → backend → DB)
4) where configuration, environment variables, and feature flags live
5) top 5 places I should read first to become productive quickly

End with a “First 60 minutes” onboarding checklist.`,
  },
  {
    label: "Debug",
    hint: "Find and fix bugs",
    icon: Bug,
    prompt: `Help me debug an issue in this project.

I want you to:
1) ask clarifying questions if needed
2) form likely root-cause hypotheses
3) identify the exact files/components to inspect first
4) propose a step-by-step debugging plan with quick validation checks
5) suggest the smallest safe fix and how to verify it

Please prioritize likely causes first and keep the plan actionable.`,
  },
  {
    label: "Refactor",
    hint: "Find refactors",
    icon: Code2,
    prompt: `Search this codebase for useful refactor opportunities.

Please:
1) inspect the repository for duplication, large/complex files, dead code, and inconsistent patterns
2) identify high-value refactor opportunities with exact file paths and why each matters
3) prioritize opportunities by impact, effort, and risk
4) propose concrete implementation steps for the top 3 opportunities
5) include validation steps (tests/checks) for each proposed refactor

Focus on practical, low-risk improvements that improve maintainability and readability.`,
  },
] as const;

export function NewChatView({
  tabId: _tabId,
  isFocused = true, // Default to focused
  onChatCreated,
}: NewChatViewProps) {
  const [isCreating, setIsCreating] = useState(false);
  const [showCreateWorktreeModal, setShowCreateWorktreeModal] = useState(false);
  const [showDiscoverWorktreeModal, setShowDiscoverWorktreeModal] = useState(false);
  const [showWorkspaceDropdown, setShowWorkspaceDropdown] = useState(false);
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState<
    string | undefined
  >(undefined);
  const [shouldShowMigrationPrompt, setShouldShowMigrationPrompt] = useState(false);
  const chatInputRef = useRef<HTMLDivElement>(null);
  const workspaceDropdownRef = useRef<HTMLDivElement>(null);

  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const switchWorktreeContext = useWorktreeStore(
    (state) => state.switchWorktreeContext
  );
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const currentProject = useProjectStore((state) => state.currentProject);
  const ensureApiKeyOrShowModal = useApiKeySetupStore(
    (state) => state.ensureApiKeyOrShowModal
  );

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

  useEffect(() => {
    // Wait until project is loaded before running migration detection.
    // Without this gate, the effect fires multiple times as propsWorktreeId,
    // selectedWorkspaceId, and currentProject?.id stabilize during hydration,
    // causing 2-3 redundant GetFileTree calls.
    if (!currentProject?.id) return;

    let cancelled = false;

    const detectMigrationSources = async () => {
      try {
        const [completedSetting, files] = await Promise.all([
          safeGetSetting(MIGRATION_COMPLETED_SETTING_KEY),
          getFileTree("/", true, selectedWorkspaceId),
        ]);

        if (cancelled) return;

        const hasMigrated = completedSetting?.value === "true";
        const hasMigrationSources = files.some((file) =>
          migrationDetectionTargets.includes(file.name)
        );

        setShouldShowMigrationPrompt(!hasMigrated || hasMigrationSources);
      } catch {
        if (!cancelled) {
          setShouldShowMigrationPrompt(true);
        }
      }
    };

    void detectMigrationSources();

    return () => {
      cancelled = true;
    };
  }, [selectedWorkspaceId, currentProject?.id]);

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

  const handleSuggestionClick = useCallback((text: string) => {
    const el = chatInputRef.current;
    if (!el) return;
    el.focus();
    // Select all existing content first, then replace with the new text
    document.execCommand("selectAll", false);
    document.execCommand("insertText", false, text);
  }, []);

  const handleMigrationClick = async () => {
    await handleCreateAndSend(
      "Help me migrate useful configuration from Claude Code, Cursor, Codex, or Windsurf into Reliant.",
      undefined,
      "builtin://migrate",
      {
        mode: "auto",
      }
    );
    await upsertStringSetting(MIGRATION_COMPLETED_SETTING_KEY, "true");
    setShouldShowMigrationPrompt(false);
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

        <div className="relative z-10 min-h-full w-full max-w-5xl mx-auto grid grid-rows-[1fr_auto]">
          <div className="flex items-center justify-center pt-20">
            <div className="w-full max-w-xl mx-auto flex flex-col items-center text-center gap-4">
              <div className="inline-flex h-[4.5rem] w-[4.5rem] items-center justify-center">
                <ReliantIcon className="h-[4.5rem] w-[4.5rem]" />
              </div>

              <div className="space-y-2">
                <h1 className="text-3xl md:text-4xl font-semibold text-foreground tracking-tight">
                  Let’s code
                </h1>
              </div>

              {/* Workspace controls */}
              <div
                className="w-full flex flex-wrap items-center justify-center gap-3"
                data-onboarding="workspace-buttons"
              >
                {/* Workspace Selector */}
                <div className="relative" ref={workspaceDropdownRef}>
                  <button
                    onClick={() => setShowWorkspaceDropdown(!showWorkspaceDropdown)}
                    className="flex items-center gap-2 px-3 py-1.5 text-xs text-muted-foreground border border-border/40 rounded-md hover:border-border hover:text-foreground transition-colors"
                  >
                    <FolderGit2 className="w-3.5 h-3.5" />
                    <span className="max-w-[120px] truncate">{selectedWorkspaceName}</span>
                    <ChevronDown className="w-3 h-3 opacity-50" />
                  </button>

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
                            <LuFolderPlus className="w-3.5 h-3.5 shrink-0 -translate-y-px" />
                            <span className="font-medium leading-none">New workspace</span>
                          </div>
                        </button>
                      </div>
                    </div>
                  )}
                </div>

                {/* Quick create workspace */}
                <button
                  onClick={() => setShowCreateWorktreeModal(true)}
                  className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-muted-foreground border border-border/40 rounded-md hover:border-border hover:text-foreground transition-colors"
                >
                  <LuFolderPlus className="w-3.5 h-3.5 shrink-0 -translate-y-px" />
                  <span className="leading-none">New workspace</span>
                </button>
              </div>

            </div>
          </div>

          {/* Prompt suggestions - short labels, richer inserted prompts */}
          <div className="w-full max-w-2xl mx-auto pb-4 md:pb-5 space-y-3">
            {shouldShowMigrationPrompt && (
              <button
                onClick={() => void handleMigrationClick()}
                className="w-full rounded-xl border border-primary/30 bg-primary/5 px-4 py-3 text-left transition-colors hover:border-primary/50 hover:bg-primary/10"
              >
                <div className="flex items-start gap-3">
                  <div className="mt-0.5 rounded-md bg-primary/10 p-2 text-primary">
                    <ArrowRightLeft className="h-4 w-4" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <p className="text-sm font-medium text-foreground">
                      Migrate from Claude Code, Cursor, Codex, or Windsurf
                    </p>
                    <p className="mt-1 text-xs leading-5 text-muted-foreground">
                      Launch a guided migration chat that inspects common config locations, summarizes what’s worth carrying over, and asks before writing Reliant files.
                    </p>
                  </div>
                </div>
              </button>
            )}

            <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-1.5 md:gap-2">
              {starterPrompts.map((starter) => {
                const Icon = starter.icon;
                return (
                  <button
                    key={starter.label}
                    onClick={() => handleSuggestionClick(starter.prompt)}
                    className="group rounded-lg border border-border/50 bg-card/70 px-3 py-2 text-left transition-colors hover:border-primary/50 hover:bg-card"
                  >
                    <div className="flex items-start gap-2">
                      <Icon className="mt-0.5 h-3.5 w-3.5 text-muted-foreground group-hover:text-primary transition-colors flex-shrink-0" />
                      <div className="min-w-0">
                        <p className="text-xs font-medium leading-4 text-foreground">{starter.label}</p>
                        <p className="mt-0.5 text-[10px] leading-4 text-muted-foreground truncate">
                          {starter.hint}
                        </p>
                      </div>
                    </div>
                  </button>
                );
              })}
            </div>
          </div>
        </div>
      </div>

      {/* Message Input - Collapsible when not focused */}
      {isFocused ? (
        <div className="flex-shrink-0">
          <ChatInput
            ref={chatInputRef}
            onSend={handleCreateAndSend}
            disabled={isCreating}
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
    </div>
  );
}