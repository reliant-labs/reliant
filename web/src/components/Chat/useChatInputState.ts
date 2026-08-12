import { useState, useEffect, useRef, useCallback } from "react";
import { useChat, seedChatDetail, patchChatCaches } from "../../hooks/chat-queries";
import { useChatParamsStore } from "../../store/chatParamsStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { DEFAULT_WORKFLOW, usePreferencesStore } from "../../store/preferencesStore";
import { chatGrpc } from "../../api/chat-grpc";

interface UseChatInputStateProps {
  chatId?: string;
  tabId?: string; // Add tabId to track which tab this input belongs to
}

export function useChatInputState({
  chatId,
  tabId,
}: UseChatInputStateProps) {
  // Get project/worktree context for draft persistence
  const currentProjectId = useProjectStore((state) => state.currentProject?.id);
  const currentWorktreeId = useWorktreeStore((state) => state.currentWorktree?.id ?? null);
  
  // For new chats (no chatId), use project-level draft storage
  // This ensures drafts persist when switching workspaces within NewChatView
  const isNewChat = !chatId;
  const draftKey = chatId || "temp-new-chat";
  
  // Basic state - initialize from persisted draft if available
  const [input, setInputRaw] = useState(() => {
    if (!currentProjectId) return "";
    
    // For new chats, use project-level draft (persists across worktree switches)
    if (isNewChat) {
      return useWorkspaceStateStore.getState().getNewChatDraft(currentProjectId);
    }
    
    // For existing chats, use worktree-level draft
    const worktreeState = useWorkspaceStateStore.getState().getWorktreeState(
      currentProjectId,
      currentWorktreeId
    );
    return worktreeState.chatDrafts?.[draftKey] || "";
  });

  // Ref for debounce timer
  const draftSaveTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Get user's default workflow from preferences
  const userDefaultWorkflow = usePreferencesStore((state) => state.preferences?.defaultWorkflow);
  const effectiveDefaultWorkflow = userDefaultWorkflow ?? DEFAULT_WORKFLOW;

  // Workflow selection state - tracks which workflow is selected.
  // null means use user's default workflow (from preferences).
  // For new chats, onboarding/starter cards can hint a one-time selection via
  // chatParamsStore.tempNewChatWorkflow.
  const [selectedWorkflow, setSelectedWorkflow] = useState<string | null>(
    () => useChatParamsStore.getState().tempNewChatWorkflow,
  );

  // Subscribe to tempNewChatWorkflow so external updates (e.g. WorkflowStarterCards
  // clicks) propagate to the composer. Without this subscription, the initial
  // useState reads the value once and never reacts to changes.
  const tempNewChatWorkflow = useChatParamsStore(
    (state) => state.tempNewChatWorkflow,
  );

  // Sync external temp workflow changes into local state for new chats.
  // Only fires when the external value actually differs from local — this
  // prevents infinite loops when the user changes workflow locally via
  // WorkflowSelector (which doesn't touch tempNewChatWorkflow).
  useEffect(() => {
    if (chatId) return; // Only sync temp value for new chats
    setSelectedWorkflow((current) =>
      current === tempNewChatWorkflow ? current : tempNewChatWorkflow,
    );
  }, [tempNewChatWorkflow, chatId]);

  // Computed values — read chat from React Query cache
  const { data: currentChat } = useChat(chatId);

  // Debounced draft save - wraps setInput to persist drafts
  const setInput = useCallback((value: string | ((prev: string) => string)) => {
    setInputRaw((prev) => {
      const newValue = typeof value === 'function' ? value(prev) : value;
      
      // Debounce saving to workspace state (300ms)
      if (draftSaveTimeoutRef.current) {
        clearTimeout(draftSaveTimeoutRef.current);
      }
      
      draftSaveTimeoutRef.current = setTimeout(() => {
        const projectId = useProjectStore.getState().currentProject?.id;
        const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
        
        if (projectId) {
          // For new chats, use project-level draft (persists across worktree switches)
          if (isNewChat) {
            if (newValue.trim()) {
              useWorkspaceStateStore.getState().setNewChatDraft(projectId, newValue);
            } else {
              useWorkspaceStateStore.getState().clearNewChatDraft(projectId);
            }
          } else {
            // For existing chats, use worktree-level draft
            if (newValue.trim()) {
              useWorkspaceStateStore.getState().setChatDraft(
                projectId,
                worktreeId,
                draftKey,
                newValue
              );
            } else {
              useWorkspaceStateStore.getState().clearChatDraft(
                projectId,
                worktreeId,
                draftKey
              );
            }
          }
        }
      }, 300);
      
      return newValue;
    });
  }, [draftKey, isNewChat]);
  
  // Clean up debounce timer on unmount
  useEffect(() => {
    return () => {
      if (draftSaveTimeoutRef.current) {
        clearTimeout(draftSaveTimeoutRef.current);
      }
    };
  }, []);
  
  // Load draft when switching chats (chatId changes)
  useEffect(() => {
    if (!currentProjectId) return;
    
    let draft: string;
    if (isNewChat) {
      // Project-level draft for new chats
      draft = useWorkspaceStateStore.getState().getNewChatDraft(currentProjectId);
    } else {
      // Worktree-level draft for existing chats
      const worktreeState = useWorkspaceStateStore.getState().getWorktreeState(
        currentProjectId,
        currentWorktreeId
      );
      draft = worktreeState.chatDrafts?.[draftKey] || "";
    }
    setInputRaw(draft);
  }, [currentProjectId, currentWorktreeId, draftKey, isNewChat]);

  // Sync workflow from currentChat when it loads/changes
  useEffect(() => {
    if (!currentChat) return;
    
    // For custom workflows, display the workflow name
    // If workflow matches user's default, use null (means "use default")
    const workflow = currentChat.workflowName === effectiveDefaultWorkflow
      ? null
      : currentChat.workflowName || null;
    
    setSelectedWorkflow(workflow);
  }, [currentChat, effectiveDefaultWorkflow, chatId, userDefaultWorkflow]);

  // Reset state when chatId or tabId changes
  useEffect(() => {
    // Reset workflow selection for new chats, but preserve any
    // one-time workflow hint set by onboarding (tempNewChatWorkflow).
    if (!chatId) {
      const temp = useChatParamsStore.getState().tempNewChatWorkflow;
      setSelectedWorkflow(temp ?? null);
    }
  }, [chatId, tabId]);

  const handleClearInput = useCallback(() => {
    setInputRaw("");
    
    // Immediately clear the draft from persistence
    const projectId = useProjectStore.getState().currentProject?.id;
    const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
    
    if (projectId) {
      // Clear any pending debounced save
      if (draftSaveTimeoutRef.current) {
        clearTimeout(draftSaveTimeoutRef.current);
        draftSaveTimeoutRef.current = null;
      }
      
      // For new chats, clear project-level draft
      if (isNewChat) {
        useWorkspaceStateStore.getState().clearNewChatDraft(projectId);
      } else {
        // For existing chats, clear worktree-level draft
        useWorkspaceStateStore.getState().clearChatDraft(
          projectId,
          worktreeId,
          draftKey
        );
      }
    }
  }, [draftKey, isNewChat]);

  // Check if chat is pending (hasn't started yet - can switch workflows)
  // A pending chat has no messages yet (lastMessageAt is unset)
  const isPendingChat = !chatId || !currentChat?.lastMessageAt;

  // Wrapped workflow setter that persists to backend for pending chats
  const handleSetSelectedWorkflow = useCallback(async (workflow: string | null) => {
    // Always update local state immediately
    setSelectedWorkflow(workflow);
    
    // If chat is pending, persist the workflow change to backend
    if (chatId && isPendingChat) {
      try {
        const workflowName = workflow ?? effectiveDefaultWorkflow;
        const updatedChat = await chatGrpc.update(chatId, { workflow_name: workflowName });
        // Home the updated chat into the React Query caches (the single source
        // of truth): replace the detail entry and patch workflowName in the
        // list so the sidebar stays in sync.
        seedChatDetail(updatedChat);
        patchChatCaches(currentProjectId, chatId, {
          workflowName: updatedChat.workflowName,
        });
      } catch (error) {
        console.error("Failed to update workflow:", error);
        // Revert local state on error
        if (currentChat?.workflowName) {
          setSelectedWorkflow(
            currentChat.workflowName === effectiveDefaultWorkflow
              ? null
              : currentChat.workflowName
          );
        }
      }
    }
  }, [chatId, isPendingChat, currentChat?.workflowName, effectiveDefaultWorkflow, currentProjectId]);

  return {
    // State
    input,
    setInput,

    // Workflow selection
    selectedWorkflow,
    setSelectedWorkflow: handleSetSelectedWorkflow,
    isPendingChat,

    // Handlers
    handleClearInput,
  };
}