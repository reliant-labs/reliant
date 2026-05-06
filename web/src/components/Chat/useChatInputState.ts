import { useState, useEffect, useRef, useCallback } from "react";
import { useChatStore } from "../../store/chatStore";
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
  const [selectedPrompts, setSelectedPrompts] = useState("");
  const [forceStreaming, setForceStreaming] = useState(false);
  
  // Ref for debounce timer
  const draftSaveTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Get user's default workflow from preferences
  const userDefaultWorkflow = usePreferencesStore((state) => state.preferences?.defaultWorkflow);
  const effectiveDefaultWorkflow = userDefaultWorkflow ?? DEFAULT_WORKFLOW;

  // Workflow selection state - tracks which workflow is selected
  // null means use user's default workflow (from preferences)
  const [selectedWorkflow, setSelectedWorkflow] = useState<string | null>(() => {
    // For existing chats, load workflow from chat data
    if (chatId) {
      const chatObj = useChatStore.getState().chats.get(chatId);
      const prefs = usePreferencesStore.getState().preferences;
      const defaultWf = prefs?.defaultWorkflow ?? DEFAULT_WORKFLOW;
      // Only set non-null if workflow differs from user's default
      if (chatObj?.workflowName && chatObj.workflowName !== defaultWf) {
        return chatObj.workflowName;
      }
    }
    // For new chats, check if onboarding set a one-time workflow selection
    const tempWorkflow = useChatParamsStore.getState().tempNewChatParams
      .__selectedWorkflow as string | undefined;
    if (tempWorkflow) {
      return tempWorkflow;
    }
    return null;
  });

  // Store hooks
  const currentProject = useProjectStore((state) => state.currentProject);
  
  // Computed values
  const currentChat = useChatStore((state) =>
    chatId ? state.chats.get(chatId) || null : null
  );
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
    // Reset workflow selection for new chats
    if (!chatId) {
      setSelectedWorkflow(null);
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
        // Update the chat store with the new workflow name
        // This matches the pattern used by renameChat in chatStore.ts
        useChatStore.setState((state) => {
          const newChats = new Map(state.chats);
          newChats.set(chatId, updatedChat);
          return { chats: newChats };
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
  }, [chatId, isPendingChat, currentChat?.workflowName, effectiveDefaultWorkflow]);

  return {
    // State
    input,
    setInput,
    selectedPrompts,
    setSelectedPrompts,
    forceStreaming,
    setForceStreaming,

    // Workflow selection
    selectedWorkflow,
    setSelectedWorkflow: handleSetSelectedWorkflow,
    isPendingChat,

    // Computed
    currentProject,

    // Handlers
    handleClearInput,
  };
}