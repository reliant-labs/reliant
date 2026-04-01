/**
 * WorkflowBuilderChat - A floating chat panel for the workflow builder
 *
 * This is a lightweight chat interface specifically for conversational workflow building.
 * It reuses the existing chat store infrastructure for streaming, message handling, etc.
 * but with a custom UI and workflow-specific tools.
 */

import { useState, useRef, useEffect, useCallback, useMemo } from "react";
import { X, Maximize2, RotateCcw } from "lucide-react";
import { ReliantIcon } from "../icons/ReliantIcon";
import { cn } from "../../lib/utils";
import type { Workflow } from "../../types/workflow";
import { BaseChatInput } from "../Chat/BaseChatInput";
import { InterleavedTimeline } from "../Chat/thread-views";
import { ChatMessagesContainer } from "../Chat/ChatMessagesContainer";
import { ChatThinkingIndicator } from "../Chat/ChatThinkingIndicator";
import { useChatStore } from "../../store/chatStore";
import {
  useChatMessages,
  useStreamingMessages,
  useChatApprovals,
  useErrorEvents,
  useInfoEvents,
  useSkillInvocations,
  useRunOutputs,
} from "../../store/chatStoreHooks";
import { useIsChatRunning, useActivityStore, ChatActivity } from "../../store/activityStore";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import { chatGrpc } from "../../api/chat-grpc";
import { getWorkflowByDraftId, getWorkflowWithDraftId, workflowGrpc } from "../../api/workflow-grpc";
import { useModels, useGlobalDataStore } from "../../store/globalDataStore";
import { useChatParamsStore } from "../../store/chatParamsStore";
import { api } from "../../api/client";
import { logger } from "../../lib/logger";
import { resolveThinkingCapabilityForModel, reconcileThinkingLevel } from "../../hooks/useThinkingCapability";
import {
  getThinkingSelectorDisplayState,
  isThinkingSelectorDisabled as getIsThinkingSelectorDisabled,
  reconcileThinkingForBuilder,
  resolveBuilderSelectedModel,
} from "./WorkflowBuilderChat.modelSelection";
import {
  ContentBlockType,
  MessageRole,
} from "../../gen/reliant/v1/chat_pb";

// Note: Tools are defined in the workflow_builder preset (internal/workflow/builtin/presets/workflow_builder.yaml)
// Includes: update_workflow, set_workflow, validate_workflow, read_workflow_state, view, grep, glob

// Storage key prefix for persisting chat IDs per workflow
const CHAT_STORAGE_KEY_PREFIX = "workflow-builder-chat:";

export type PanelSize = "normal" | "maximized";

interface WorkflowBuilderChatProps {
  /** Current workflow definition being edited */
  workflow: Workflow;
  /** Callback when the workflow is modified by the assistant */
  onWorkflowChange: (workflow: Workflow) => void;
  /** Project ID for the chat session */
  projectId: string;
  /** Whether this is a new workflow (to clear stale chat state) */
  isNewWorkflow?: boolean;
  /** Whether the chat panel is open (controlled) */
  isOpen: boolean;
  /** Callback when panel open state changes */
  onOpenChange: (isOpen: boolean) => void;
  /** Current panel size (controlled) */
  panelSize: PanelSize;
  /** Callback when panel size changes */
  onPanelSizeChange: (size: PanelSize) => void;
  /** Chat ID associated with this workflow (from database) - takes priority over localStorage */
  builderChatId?: string;
  /** Session ID for new/unsaved workflows (for localStorage key) */
  workflowSessionId?: string;
  /** Callback when a new chat is created */
  onChatIdChange?: (chatId: string) => void;
  /** Whether the config panel is currently open (to adjust button positioning) */
  isConfigPanelOpen?: boolean;
  /** Draft ID for refetching workflow after LLM edits */
  draftId?: string;
  /** Callback when draft ID changes (when backend creates a new draft) */
  onDraftIdChange?: (draftId: string) => void;
  /** Callback when workflow version changes (for OCC) */
  onVersionChange?: (version: number) => void;
}

// The workflow_builder preset contains the comprehensive system prompt with full documentation.
// It's auto-generated from repo-local generated docs during build (see: make generate-workflow-builder-preset).
// Source: tools/docgen/assembler/main.go -> internal/workflow/builtin/presets/workflow_builder.yaml
const WORKFLOW_BUILDER_PRESET = "workflow_builder";

/**
 * Get storage key for a workflow's chat
 */
function getChatStorageKey(projectId: string, workflowName: string): string {
  return `${CHAT_STORAGE_KEY_PREFIX}${projectId}:${workflowName}`;
}

/**
 * Load persisted chat ID for a workflow
 */
function loadPersistedChatId(
  projectId: string,
  workflowName: string,
): string | null {
  try {
    return localStorage.getItem(getChatStorageKey(projectId, workflowName));
  } catch {
    return null;
  }
}

/**
 * Save chat ID for a workflow
 */
function persistChatId(
  projectId: string,
  workflowName: string,
  chatId: string,
): void {
  try {
    localStorage.setItem(getChatStorageKey(projectId, workflowName), chatId);
  } catch {
    // Ignore storage errors
  }
}

/**
 * Clear persisted chat ID for a workflow
 */
function clearPersistedChatId(projectId: string, workflowName: string): void {
  try {
    localStorage.removeItem(getChatStorageKey(projectId, workflowName));
  } catch {
    // Ignore storage errors
  }
}



export function WorkflowBuilderChat({
  workflow,
  onWorkflowChange,
  projectId,
  isNewWorkflow = false,
  isOpen,
  onOpenChange,
  panelSize,
  onPanelSizeChange,
  builderChatId,
  workflowSessionId,
  onChatIdChange,
  isConfigPanelOpen = false,
  draftId,
  onDraftIdChange,
  onVersionChange,
}: WorkflowBuilderChatProps) {
  const [inputValue, setInputValue] = useState("");
  // Don't initialize from localStorage - validate it first in useEffect
  const [chatId, setChatId] = useState<string | null>(null);
  const [isSending, setIsSending] = useState(false);
  const [isValidatingChat, setIsValidatingChat] = useState(true);
  // Track if current chat was restored from persistence (workflow likely complete)
  const isRestoredChatRef = useRef(false);

  // For localStorage persistence, use workflowSessionId (for new workflows) or workflow name (for saved workflows)
  // workflowSessionId is generated when creating a new workflow and provides a stable key even before save
  const persistenceKey = workflowSessionId || workflow.name || 'default';
  const persistenceKeyRef = useRef<string>(persistenceKey);

  const inputRef = useRef<HTMLDivElement>(null);
  const hasAttemptedCatalogRecoveryRef = useRef(false);

  // Use chat store hooks for reactive updates
  const storeMessages = useChatMessages(chatId ?? undefined);
  const isChatBusy = useIsChatRunning(chatId ?? "");
  const streamingMessages = useStreamingMessages(chatId ?? "");
  const approvals = useChatApprovals(chatId ?? "");
  const errorEvents = useErrorEvents(chatId ?? "");
  const infoEvents = useInfoEvents(chatId ?? "");
  const skillInvocations = useSkillInvocations(chatId ?? "");
  const runOutputs = useRunOutputs(chatId ?? "");

  // Thinking level param (synced via chatParamsStore + updateWorkflowParams)
  const thinkingLevel = useChatParamsStore((state) => {
    if (chatId) {
      return (state.chatParams[chatId]?.thinking_level as string) ?? "medium";
    }
    return (state.tempNewChatParams.thinking_level as string) ?? "medium";
  });

  const setThinkingLevel = useCallback((level: string) => {
    if (chatId) {
      useChatParamsStore.getState().updateChatParams(chatId, { thinking_level: level });
      // Sync to running workflow immediately
      api.chatsV2.updateWorkflowParams(chatId, { thinking_level: level }).catch(() => {
        // Silent fail - UI state is already updated
      });
    } else {
      useChatParamsStore.getState().updateTempNewChatParams({ thinking_level: level });
    }
  }, [chatId]);

  // Get models for selected model lookup and default model fallback
  const { models, loading: modelsLoading, error: modelsError } = useModels();
  const isGlobalDataInitialized = useGlobalDataStore((state) => state.isInitialized);
  const isGlobalDataPrefetching = useGlobalDataStore((state) => state.isPrefetching);
  const refetchModels = useGlobalDataStore((state) => state.refetchModels);

  // Get default model: first available model
  const defaultModel = useMemo(() => {
    return models.length > 0 ? models[0].id : undefined;
  }, [models]);

  const selectedModelParamId = useChatParamsStore((state) => {
    const modelValue = chatId
      ? state.chatParams[chatId]?.model
      : state.tempNewChatParams.model;
    if (typeof modelValue === "object" && modelValue !== null) {
      return (modelValue as { id?: string }).id;
    }
    return undefined;
  });

  const {
    selectedModelId,
    canResolveThinkingCapability,
  } = useMemo(
    () =>
      resolveBuilderSelectedModel(
        selectedModelParamId,
        defaultModel,
        models,
      ),
    [selectedModelParamId, defaultModel, models],
  );

  // Catalog loading/availability state for model-aware UX and reconciliation gating.
  const isCatalogLoading =
    modelsLoading || isGlobalDataPrefetching || !isGlobalDataInitialized;
  const hasUsableModelCatalog = models.length > 0;

  const thinkingCapability = useMemo(
    () => resolveThinkingCapabilityForModel(selectedModelId, models),
    [selectedModelId, models],
  );
  const supportedThinkingLevels = thinkingCapability.levels;

  useEffect(() => {
    const fallback = reconcileThinkingForBuilder(
      thinkingLevel,
      thinkingCapability,
      canResolveThinkingCapability,
      reconcileThinkingLevel,
    );

    if (fallback !== thinkingLevel) {
      setThinkingLevel(fallback);
    }
  }, [
    canResolveThinkingCapability,
    thinkingCapability,
    thinkingLevel,
    setThinkingLevel,
  ]);

  // Chat store actions
  const initChatState = useChatStore((state) => state.initChatState);
  const subscribeToChatDetails = useGlobalUpdatesStore((state) => state.subscribeToChatDetails);
  const unsubscribeFromChatDetails = useGlobalUpdatesStore((state) => state.unsubscribeFromChatDetails);
  const sendMessage = useChatStore((state) => state.sendMessage);
  const loadMessages = useChatStore((state) => state.loadMessages);

  // Combine loading states
  const isLoading = isSending || isChatBusy;

  // Recover model catalog when panel opens and models are missing.
  // Guarded to a single recovery attempt per open cycle to avoid refetch loops
  // when users intentionally have no configured providers.
  useEffect(() => {
    if (!isOpen) {
      hasAttemptedCatalogRecoveryRef.current = false;
      return;
    }
    if (isCatalogLoading) return;
    if (hasUsableModelCatalog) {
      hasAttemptedCatalogRecoveryRef.current = false;
      return;
    }
    if (hasAttemptedCatalogRecoveryRef.current) {
      return;
    }

    hasAttemptedCatalogRecoveryRef.current = true;
    void refetchModels();
  }, [
    isOpen,
    isCatalogLoading,
    hasUsableModelCatalog,
    refetchModels,
  ]);

  // React to provider/API-key updates so model-aware controls recover without refresh.
  useEffect(() => {
    const handleApiKeySaved = () => {
      void refetchModels();
    };

    window.addEventListener("api-key-saved", handleApiKeySaved);
    return () => window.removeEventListener("api-key-saved", handleApiKeySaved);
  }, [refetchModels]);

  // Validate and load persisted chat on mount
  // Priority order:
  // 1. builderChatId from props (database) - highest priority for saved workflows
  // 2. localStorage (using workflowSessionId for new workflows, workflow.name for existing)
  useEffect(() => {
    const validateAndLoadPersistedChat = async () => {
      setIsValidatingChat(true);

      // If creating a new workflow, clear any old persisted chat and start fresh
      if (isNewWorkflow && !builderChatId) {
        // Clear using the current persistence key (which may be the workflowSessionId)
        clearPersistedChatId(projectId, persistenceKeyRef.current);
        isRestoredChatRef.current = false;
        lastProcessedOrdinalRef.current = -1;
        setChatId(null);
        setIsValidatingChat(false);
        return;
      }

      // First, check if we have a builderChatId from the database (saved workflow)
      // This takes priority over localStorage
      if (builderChatId) {
        try {
          const chat = await chatGrpc.get(builderChatId);
          if (chat) {
            isRestoredChatRef.current = true;
            initChatState(chat);
            setChatId(builderChatId);
            setIsValidatingChat(false);
            return;
          }
        } catch (error) {
          // Fall through to localStorage check
        }
      }

      // Fall back to localStorage
      const persistedChatId = loadPersistedChatId(
        projectId,
        persistenceKeyRef.current,
      );

      if (!persistedChatId) {
        // No persisted chat, start fresh
        setIsValidatingChat(false);
        return;
      }

      try {
        // Try to fetch the chat to validate it exists
        const chat = await chatGrpc.get(persistedChatId);
        if (chat) {
          // Chat exists, use it
          isRestoredChatRef.current = true;
          initChatState(chat);
          setChatId(persistedChatId);
        }
      } catch (error) {
        // Chat doesn't exist or is invalid, clear the persisted ID
        clearPersistedChatId(projectId, persistenceKeyRef.current);
        setChatId(null);
      } finally {
        setIsValidatingChat(false);
      }
    };

    validateAndLoadPersistedChat();
    // Re-run when builderChatId changes (workflow was saved and now has a DB chat ID)
  }, [projectId, initChatState, isNewWorkflow, builderChatId]);

  // Start streaming and load messages when we have a chat ID
  useEffect(() => {
    if (chatId && !isValidatingChat) {
      // Start websocket for streaming updates
      subscribeToChatDetails(chatId);

      // Also load messages explicitly - this handles:
      // 1. Returning to an existing chat (messages already in DB)
      // 2. Race condition where workflow completes before websocket connects
      loadMessages(chatId);

      // Note: Auto-approve is now handled via workflow params (mode: "auto")
      // passed when creating/sending messages, not as separate toggle
      return () => {
        unsubscribeFromChatDetails();
      };
    }
  }, [chatId, isValidatingChat, subscribeToChatDetails, unsubscribeFromChatDetails, loadMessages]);

  // Refresh messages when workflow finishes to catch any that weren't streamed
  // This handles the race condition where websocket connects mid-workflow
  const prevIsChatBusyRef = useRef(isChatBusy);
  useEffect(() => {
    // When chat transitions from busy to not busy, reload messages
    // to ensure we have all messages including those written after websocket connected
    if (prevIsChatBusyRef.current && !isChatBusy && chatId) {
      loadMessages(chatId);
    }
    prevIsChatBusyRef.current = isChatBusy;
  }, [isChatBusy, chatId, loadMessages]);

  // Focus input when panel opens
  useEffect(() => {
    if (isOpen) {
      setTimeout(() => inputRef.current?.focus(), 100);
    }
  }, [isOpen]);

  // Watch for workflow updates from tool results
  // Track the highest ordinal we've processed to avoid re-processing on remount
  const lastProcessedOrdinalRef = useRef<number>(-1);
  // Track if we've applied the initial workflow for restored chats
  const hasAppliedRestoredWorkflowRef = useRef(false);

  useEffect(() => {
    if (!storeMessages.length) return;

    // For RESTORED chats on first mount, refetch from draft to sync workflow state
    // Only run this path ONCE - mark as processed even if draftId is missing to prevent
    // re-triggering when draftId becomes available later
    if (isRestoredChatRef.current && !hasAppliedRestoredWorkflowRef.current) {
      hasAppliedRestoredWorkflowRef.current = true;

      // Set lastProcessedOrdinal to skip existing messages (do this regardless of draftId)
      const maxOrdinal = Math.max(...storeMessages.map((m) => Number(m.ordinal || 0)));
      lastProcessedOrdinalRef.current = maxOrdinal;

      if (!draftId) {
        return;
      }

      // Refetch latest workflow from draft
      getWorkflowByDraftId(projectId, draftId)
        .then(({ workflow: updatedWorkflow, version }) => {
          if (updatedWorkflow) {
            onWorkflowChange(updatedWorkflow);
          }
          // Update version for OCC
          if (version && onVersionChange) {
            onVersionChange(version);
          }
        })
        .catch((error) => {
          logger.error("[WorkflowBuilderChat] Failed to refetch workflow:", error);
        });

      return;
    }

    // Watch for NEW successful edit_workflow/write_workflow tool results
    // When detected, refetch the workflow (by draft ID or by name when draftId is missing)

    for (let i = storeMessages.length - 1; i >= 0; i--) {
      const msg = storeMessages[i];
      if (msg.role !== MessageRole.TOOL) continue;

      // Skip if we've already processed this message (by ordinal)
      const ordinal = Number(msg.ordinal || 0);
      if (ordinal <= lastProcessedOrdinalRef.current) {
        continue;
      }

      const blocks = msg.contentBlocks ?? [];

      for (const block of blocks) {
        if (block.type !== ContentBlockType.TOOL_RESULT) continue;

        // Check if this is an edit_workflow or write_workflow tool result
        const toolName = block.toolName;
        const isWorkflowTool =
          toolName === "edit_workflow" || toolName === "write_workflow";

        if (!isWorkflowTool) continue;

        // Check if the tool call was successful (not an error)
        if (block.isError) {
          continue;
        }

        // Mark as processed BEFORE refetching to prevent loops
        lastProcessedOrdinalRef.current = ordinal;

        if (draftId) {
          // Refetch by draft ID
          getWorkflowByDraftId(projectId, draftId)
            .then(({ workflow: updatedWorkflow, version }) => {
              if (updatedWorkflow) {
                onWorkflowChange(updatedWorkflow);
              }
              if (version && onVersionChange) {
                onVersionChange(version);
              }
            })
            .catch((error) => {
              logger.error("[WorkflowBuilderChat] Failed to refetch workflow:", error);
            });
        } else if (workflow?.name) {
          // No draftId (e.g. builtin/unsaved): refetch by workflow name and adopt draftId if returned
          getWorkflowWithDraftId(projectId, workflow.name)
            .then(({ workflow: updatedWorkflow, draftId: newDraftId, version }) => {
              if (updatedWorkflow) {
                onWorkflowChange(updatedWorkflow);
              }
              if (newDraftId && onDraftIdChange) {
                onDraftIdChange(newDraftId);
              }
              if (version != null && onVersionChange) {
                onVersionChange(version);
              }
            })
            .catch((error) => {
              logger.error("[WorkflowBuilderChat] Failed to refetch workflow by name:", error);
            });
        }

        return;
      }
    }
  }, [storeMessages, onWorkflowChange, projectId, draftId, onVersionChange, workflow?.name, onDraftIdChange]);

  const handleSend = useCallback(async () => {
    if (!inputValue.trim() || isLoading) return;

    const userContent = inputValue.trim();
    // Build system message for draft ID context (properly hidden from UI)
    const systemMessageContent = draftId
      ? `You are operating on workflow ${draftId}. Use this ID for all workflow operations.`
      : null;
    const systemInputMessage = systemMessageContent
      ? {
          role: MessageRole.SYSTEM,
          content: systemMessageContent,
        }
      : null;
    const systemStoreMessage = systemMessageContent
      ? {
          role: "system" as const,
          content: systemMessageContent,
        }
      : null;

    setInputValue("");
    setIsSending(true);

    try {
      if (!chatId) {
        // First message: create chat (LLM will use view_workflow tool to read current state)
        // Build messages array with optional system message first, then user message
        const messages: Array<{ role: MessageRole; content: string }> = [];
        if (systemInputMessage) {
          messages.push(systemInputMessage);
        }
        messages.push({ role: MessageRole.USER, content: userContent });

        const result = await chatGrpc.create({
          project_id: projectId,
          messages,
          workflow: "builtin://agent",
          selected_presets: { "": WORKFLOW_BUILDER_PRESET },
          workflow_params: {
            mode: "auto",
            thinking_level: thinkingLevel,
            ...(selectedModelId && { model: { id: selectedModelId } }),
          },
        });

        const newChatId = result.chat.id;
        isRestoredChatRef.current = false; // New chat, not restored
        lastProcessedOrdinalRef.current = -1; // Reset for new chat
        setChatId(newChatId);

        // Persist the chat ID to localStorage (backup for unsaved workflows)
        persistChatId(projectId, persistenceKeyRef.current, newChatId);

        // Notify parent of new chat ID (for database persistence when workflow is saved)
        onChatIdChange?.(newChatId);

        // Associate chat with draft so tools can find it
        if (draftId) {
          try {
            await workflowGrpc.associateChatWithWorkflowDraft(newChatId, draftId);
          } catch (err) {
            logger.error("[WorkflowBuilderChat] Failed to associate chat with draft:", err);
          }
        }

        // Initialize chat state in store
        initChatState(result.chat);

        // Transfer temp params (e.g. thinking_level) to the real chat
        useChatParamsStore.getState().transferTempToChat(newChatId);

        // Load messages explicitly to avoid race condition where workflow completes
        // before websocket connects and streaming starts
        await loadMessages(newChatId);
        // Note: subscribeToChatDetails is called by the useEffect when chatId changes
      } else {
        // Subsequent message: use store's sendMessage
        // Note: mode is passed as workflow param for auto-approve behavior
        await sendMessage(chatId, userContent, undefined, {
          selectedPresets: { "": WORKFLOW_BUILDER_PRESET },
          workflowParams: {
            mode: "auto",
            thinking_level: thinkingLevel,
            ...(selectedModelId && { model: { id: selectedModelId } }),
          },
          // Pass system message separately for proper handling (hidden from UI)
          ...(systemStoreMessage && { systemMessages: [systemStoreMessage] }),
        });
      }
    } catch (error) {
      logger.error("[WorkflowBuilderChat] Chat error:", error);
    } finally {
      setIsSending(false);
    }
  }, [
    inputValue,
    isLoading,
    chatId,
    projectId,
    draftId,
    initChatState,
    sendMessage,
    loadMessages,
    selectedModelId,
    thinkingLevel,
    onChatIdChange,
  ]);



  const handleStop = useCallback(async () => {
    if (!chatId || !isLoading) return;

    // Optimistically set activity to IDLE so the UI unblocks immediately
    // (matches the pattern used by chatStore.cancelChat)
    useActivityStore.getState().setActivity(chatId, ChatActivity.IDLE);

    try {
      await chatGrpc.cancel(chatId);
    } catch (error) {
      logger.error("[WorkflowBuilderChat] Failed to stop chat:", error);
    }
  }, [chatId, isLoading]);

  const togglePanel = useCallback(() => {
    if (!isOpen) {
      onOpenChange(true);
      onPanelSizeChange("normal");
    } else {
      onOpenChange(false);
    }
  }, [isOpen, onOpenChange, onPanelSizeChange]);

  const toggleSize = useCallback(() => {
    onPanelSizeChange(panelSize === "normal" ? "maximized" : "normal");
  }, [panelSize, onPanelSizeChange]);

  const handleReset = useCallback(async () => {
    // Stop websocket if running
    if (chatId) {
      unsubscribeFromChatDetails();
    }
    // Clear persisted chat ID from localStorage
    clearPersistedChatId(projectId, persistenceKeyRef.current);
    // Reset local state
    setChatId(null);
    isRestoredChatRef.current = false;
    lastProcessedOrdinalRef.current = -1;
    hasAppliedRestoredWorkflowRef.current = false;
    // Notify parent to clear chat ID from workflow draft
    onChatIdChange?.("");
  }, [chatId, projectId, unsubscribeFromChatDetails, onChatIdChange]);

  // Panel dimensions based on size
  const panelDimensions = useMemo(() => {
    return panelSize === "maximized"
      ? "h-[80vh] w-[600px]"
      : "h-[500px] w-[400px]";
  }, [panelSize]);

  // Merge streaming messages with store messages (same pattern as ChatContainer)
  const messages = useMemo(() => {
    if (streamingMessages.length === 0) return storeMessages;
    const msgs = [...storeMessages];
    for (const streamingMsg of streamingMessages) {
      const existingIdx = msgs.findIndex((m) => m.id === streamingMsg.id);
      if (existingIdx >= 0) {
        msgs[existingIdx] = streamingMsg;
      } else {
        msgs.push(streamingMsg);
      }
    }
    return msgs;
  }, [storeMessages, streamingMessages]);

  // Filter out tool-role messages (InterleavedTimeline renders tool results inline)
  const filteredMessages = useMemo(
    () => messages.filter((m) => m.role !== MessageRole.TOOL),
    [messages],
  );

  // Welcome message when no chat exists (and not validating)
  const showWelcome =
    !chatId && filteredMessages.length === 0 && !isValidatingChat;

  if (!isOpen) {
    // Floating button to open chat
    // Compact version when config panel is open - positioned between toolbar and panel
    // Config panel is w-96 (384px) + right-6 (24px) = 408px from right edge
    if (isConfigPanelOpen) {
      return (
        <button
          onClick={togglePanel}
          className={cn(
            "fixed bottom-6 right-[440px] z-50",
            "flex items-center justify-center gap-2 px-4 py-3",
            "bg-gradient-to-r from-blue-500 to-purple-500 border border-blue-400/50",
            "text-white",
            "rounded-full shadow-lg",
            "hover:from-blue-600 hover:to-purple-600 transition-all",
            "hover:scale-105",
          )}
          title="AI Assistant"
        >
          <ReliantIcon className="w-5 h-5 brightness-0 invert" />
          <span className="font-medium text-sm">AI</span>
        </button>
      );
    }

    return (
      <button
        onClick={togglePanel}
        className={cn(
          "fixed bottom-6 right-6 z-50",
          "flex items-center gap-3 px-6 py-4",
          "bg-gradient-to-r from-blue-500 to-purple-500 border border-blue-400/50",
          "text-white text-base",
          "rounded-full shadow-lg",
          "hover:from-blue-600 hover:to-purple-600 transition-all",
          "hover:scale-105",
        )}
      >
        <ReliantIcon className="w-6 h-6 brightness-0 invert" />
        <span className="font-medium">AI Assistant</span>
      </button>
    );
  }

  const thinkingSelectorDisplayState = getThinkingSelectorDisplayState({
    isCatalogLoading,
    modelsError,
    canResolveThinkingCapability,
  });

  const isThinkingSelectorDisabled = getIsThinkingSelectorDisabled({
    isCatalogLoading,
    canResolveThinkingCapability,
    modelsError,
  });

  return (
    <div
      className={cn(
        "fixed bottom-6 right-6 z-50",
        "flex flex-col",
        "bg-background border border-border rounded-xl shadow-2xl",
        "transition-all duration-200",
        panelDimensions,
      )}
      data-onboarding="workflow-chat"
    >
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-border bg-muted/30 rounded-t-xl">
        <div className="flex items-center gap-2">
          <ReliantIcon className="w-4 h-4 text-primary" />
          <span className="font-medium text-sm">Workflow Assistant</span>
        </div>
        <div className="flex items-center gap-2">
          {/* Reset chat button */}
          {chatId && (
            <button
              onClick={handleReset}
              className="p-1.5 hover:bg-muted rounded-md transition-colors text-muted-foreground hover:text-foreground"
              title="Reset chat"
            >
              <RotateCcw className="w-4 h-4" />
            </button>
          )}
          {/* Thinking level toggle */}
          <select
            value={thinkingLevel}
            onChange={(e) => setThinkingLevel(e.target.value)}
            disabled={isThinkingSelectorDisabled}
            className="text-xs bg-transparent border border-border rounded px-1.5 py-0.5 text-muted-foreground hover:text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-60 disabled:cursor-not-allowed"
            title={
              thinkingSelectorDisplayState === "loading"
                ? "Loading model capabilities..."
                : thinkingSelectorDisplayState === "error"
                  ? "Model catalog unavailable"
                  : "Thinking level"
            }
          >
            {thinkingSelectorDisplayState === "loading" && (
              <option value="">Loading models...</option>
            )}
            {thinkingSelectorDisplayState === "error" && (
              <option value="">Model catalog unavailable</option>
            )}
            {thinkingSelectorDisplayState === "empty" && (
              <option value="">No models available</option>
            )}
            {thinkingSelectorDisplayState === "ready" && (
              <option value="">No thinking</option>
            )}
            {supportedThinkingLevels.includes("low") && <option value="low">Low</option>}
            {supportedThinkingLevels.includes("medium") && <option value="medium">Medium</option>}
            {supportedThinkingLevels.includes("high") && <option value="high">High</option>}
            {supportedThinkingLevels.includes("xhigh") && <option value="xhigh">XHigh</option>}
          </select>
          <button
            onClick={toggleSize}
            className="p-1.5 hover:bg-muted rounded-md transition-colors"
            title={panelSize === "maximized" ? "Restore" : "Maximize"}
          >
            <Maximize2 className="w-4 h-4" />
          </button>
          <button
            onClick={() => onOpenChange(false)}
            className="p-1.5 hover:bg-muted rounded-md transition-colors"
            title="Close"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* Messages */}
      {showWelcome ? (
        <div className="flex-1 flex items-center justify-center p-4">
          <div className="text-center text-sm text-muted-foreground">
            Describe what you want to build...
          </div>
        </div>
      ) : (
        <ChatMessagesContainer
          chatId={chatId ?? undefined}
          className="flex-1 min-h-0"
        >
          <InterleavedTimeline
            messages={filteredMessages}
            approvals={approvals}
            errorEvents={errorEvents}
            infoEvents={infoEvents}
            skillInvocations={skillInvocations}
            runOutputs={runOutputs}
            chatId={chatId || ""}
            isStreaming={isLoading}
            footer={chatId && approvals.length === 0 ? (
              <ChatThinkingIndicator chatId={chatId} />
            ) : undefined}
          />
        </ChatMessagesContainer>
      )}

      {/* Input */}
      <div className="p-3 border-t border-border">
        <BaseChatInput
          ref={inputRef}
          value={inputValue}
          onChange={setInputValue}
          onSend={handleSend}
          onStop={handleStop}
          disabled={isValidatingChat}
          isLoading={isLoading}
          placeholder="Describe what you want to build..."
          maxHeight={200}
          showHint={false}
          inlineSendButton
        />
      </div>
    </div>
  );
}
