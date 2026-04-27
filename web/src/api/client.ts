import { getCatalogClient } from "./grpc-client";
import { projectGrpc } from "./project-grpc";
import { approvalGrpc } from "./approval-grpc";
import { chatGrpc } from "./chat-grpc";
import type { Chat, Message } from "../types/chat";
import { ChatState, MessageRole } from "../types/chat";
import { settingsGrpc, type Preferences } from "./settings-grpc";
import { mcpGrpc } from "./mcp-grpc";
import { toolCallGrpc } from "./tool-call-grpc";
import { logger } from "../lib/logger";
import { HiddenItemType } from "../gen/reliant/v1/settings_pb";
import type {
  CreateChatRequest,
  UpdateChatRequest,
  BranchChatRequest,
} from "../types/api";

// Re-export types for consumers
export type { Chat, Message, ContentBlock, Attachment } from "../types/chat";
export { ChatState } from "../types/chat";

// Re-export approval types from canonical source
export type { ToolApprovalRequest } from "./approval-grpc";

export const api = {
  workflows: {
    list: async (projectId: string) => {
      const { workflowGrpc } = await import("./workflow-grpc");
      const workflows = await workflowGrpc.listWorkflows(projectId);
      return {
        workflows: workflows.map((w) => ({
          name: w.name,
          filename: w.filename,
          description: typeof w.description === 'string' ? w.description : "",
          step_count: w.stepCount,
          source: w.source,
          is_hidden: w.isHidden || false,
          is_valid: w.isValid !== false, // Default to true for builtin/project workflows
        })),
        total: workflows.length,
      };
    },
  },

  models: {
    list: async () => {
      const client = getCatalogClient();
      const response = await client.listModels({});
      return {
        models: response.models.map((m) => ({
          id: m.id,
          name: m.name,
          provider: m.provider,
          driverId: m.driverId,
          canReason: m.canReason,
          supportedThinkingLevels: m.supportedThinkingLevels,
          capabilities: m.capabilities,
          metadata: undefined,
        })),
        total: response.total,
      };
    },
    listByProvider: async (provider: string) => {
      const client = getCatalogClient();
      const response = await client.listModelsByProvider({ provider });
      return response.models.map((m) => ({
        id: m.id,
        name: m.name,
        provider: m.provider,
        contextWindow: Number(m.contextWindow),
        defaultMaxTokens: Number(m.defaultMaxTokens),
        costPer1MIn: m.costPer1mIn,
        costPer1MOut: m.costPer1mOut,
      }));
    },
  },

  tools: {
    list: async () => {
      const client = getCatalogClient();
      const response = await client.listTools({});
      return {
        tools: response.tools.map((t) => ({
          name: t.name,
          description: t.description,
          category: t.category,
        })),
        total: response.total,
      };
    },
  },

  chatsV2: {
    create: async (request: CreateChatRequest) => {
      const result = await chatGrpc.create({
        project_id: request.project_id!,
        messages: request.messages,
        title: request.title,
        worktree_id: request.worktree_id,
        workflow: request.workflow,
        attachments: request.attachments,
        workflow_params: request.workflow_params,
        selected_presets: request.selectedPresets,
      });
      return result.chat;
    },

    list: async (projectId?: string) => {
      if (!projectId) {
        return { chats: [], lastUserUpdateSequence: 0 };
      }
      const result = await chatGrpc.list(projectId);
      return { chats: result.chats, lastUserUpdateSequence: result.lastUserUpdateSequence };
    },

    search: async (projectId: string, searchQuery: string) => {
      if (!projectId) {
        throw new Error("projectId is required for search");
      }
      if (!searchQuery) {
        return [];
      }
      const result = await chatGrpc.search(projectId, searchQuery);
      return result.chats;
    },

    get: async (id: string) => {
      return chatGrpc.get(id);
    },

    update: async (id: string, request: UpdateChatRequest) => {
      return chatGrpc.update(id, {
        title: request.title,
        worktree_id: request.worktree_id,
      });
    },

    delete: async (id: string) => {
      return chatGrpc.delete(id);
    },

    updateState: async (id: string, state: ChatState) => {
      const result = await chatGrpc.updateState(id, state);
      return {
        message: result.message,
        state: result.state,
        chat_id: id,
      };
    },

    archive: async (id: string) => {
      return chatGrpc.archive(id);
    },

    unarchive: async (id: string, _restoreWorktree?: boolean) => {
      // Worktree restoration is handled automatically on backend
      return chatGrpc.unarchive(id);
    },

    // Returns chats with worktree metadata flattened in
    listArchived: async () => {
      const result = await chatGrpc.listArchived();
      // Flatten ArchivedChat metadata into the Chat object
      return result.chats.map((ac) => ({
        ...ac.chat,
        worktreeName: ac.worktreeName,
        worktreeDeletedAt: ac.worktreeDeletedAt,
      } as Chat));
    },

    cancel: async (chatId: string) => {
      const result = await chatGrpc.cancel(chatId);
      return {
        status: result.success ? "cancelled" : "failed",
        message: result.message,
        chat_id: chatId,
      };
    },

    pause: async (chatId: string) => {
      return chatGrpc.pause(chatId);
    },

    resume: async (chatId: string) => {
      return chatGrpc.resume(chatId);
    },

    branch: async (
      id: string,
      request: BranchChatRequest & { worktreeId?: string }
    ) => {
      const result = await chatGrpc.branch(id, {
        messageId: request.messageId,
        title: request.title,
        worktreeId: request.worktreeId,
      });
      return {
        chat: result.chat,
      };
    },

    dismiss: async (id: string) => {
      return chatGrpc.dismiss(id);
    },

    markUnread: async (id: string) => {
      return chatGrpc.markUnread(id);
    },

    compact: async (chatId: string, threadId: string) => {
      return chatGrpc.compact(chatId, threadId);
    },

    updateWorkflowParams: async (chatId: string, params: Record<string, unknown>, threadId?: string) => {
      return chatGrpc.updateWorkflowParams(chatId, params, threadId);
    },

    sendMessage: async (
      chatId: string,
      content: string,
      attachments?: string[],
      options?: {
        workflow?: string | null;
        mode?: string;
        temperature?: number;
        max_tokens?: number;
        workflow_params?: Record<string, unknown>;
        target_thread?: string;
        selected_presets?: Record<string, string>;
        systemMessages?: Array<{ content: string }>; // Optional system messages to prepend
        discuss?: boolean; // If true, chat with LLM without resuming paused workflow
      }
    ) => {
      // Build messages array: system messages first, then user message
      const messages: Array<{ role: MessageRole; content: string }> = [];
      if (options?.systemMessages) {
        messages.push(...options.systemMessages.map(m => ({ role: MessageRole.SYSTEM, content: m.content })));
      }
      if (content) {
        messages.push({ role: MessageRole.USER, content });
      }

      logger.info("[api.chatsV2.sendMessage] DEBUG options:", { workflow_params: options?.workflow_params });
      const result = await chatGrpc.sendMessage(chatId, {
        messages,
        attachments,
        workflow: options?.workflow ?? undefined,
        mode: options?.mode,
        temperature: options?.temperature,
        max_tokens: options?.max_tokens,
        workflow_params: options?.workflow_params,
        target_thread: options?.target_thread,
        selected_presets: options?.selected_presets,
        discuss: options?.discuss,
      });
      // Return workflow metadata for state updates
      return {
        chatId: result.chat_id,
        workflowId: result.workflow_id,
        runId: result.run_id,
      };
    },

    listMessages: async (chatId: string, options?: { recent?: number; beforeOrdinal?: number }) => {
      const result = await chatGrpc.listMessages(chatId, {
        recent: options?.recent,
        before_ordinal: options?.beforeOrdinal,
      });
      return {
        messages: result.messages as Message[],
        total: result.total,
        hasMore: result.has_more,
        oldestOrdinal: result.oldest_ordinal,
      };
    },
  },

  health: async () => {
    const { systemGrpc } = await import("./system-grpc");
    return systemGrpc.health();
  },

  approvals: {
    listByChat: async (chatId: string) =>
      approvalGrpc.listByChat(chatId),

    approve: async (requestId: string, actionTaken?: string) =>
      approvalGrpc.approve(requestId, actionTaken),

    deny: async (
      requestId: string,
      denialReason?: string,
      actionTaken?: string
    ) => approvalGrpc.deny(requestId, denialReason, actionTaken),

    batchApprove: async (
      requestIds: string[],
      actionTaken?: string
    ) => approvalGrpc.batchApprove(requestIds, actionTaken),

    batchDeny: async (
      requestIds: string[],
      denialReason?: string,
      actionTaken?: string
    ) => approvalGrpc.batchDeny(requestIds, denialReason, actionTaken),
  },

  toolCalls: {
    cancel: (toolCallId: string) => toolCallGrpc.cancel(toolCallId),
    convertToBackground: (toolCallId: string) =>
      toolCallGrpc.convertToBackground(toolCallId),
  },

  settings: {
    getShortcuts: async () => {
      const shortcuts = await settingsGrpc.getShortcuts();
      return { shortcuts };
    },

    updateShortcuts: async (shortcuts: string) => {
      const result = await settingsGrpc.updateShortcuts(shortcuts);
      return { message: result.message };
    },

    getPreferences: async () => {
      const prefs = await settingsGrpc.getPreferences();
      // Return strongly typed - settings-grpc.ts has proper Preferences interface
      return prefs;
    },

    updatePreferences: async (preferences: Record<string, unknown>) => {
      // Extract known fields and put rest in additional
      const {
        streaming_enabled,
        worktree_archive_mode,
        worktree_default_delete_directory,
        worktree_default_delete_branch,
        default_mcp_scope,
        default_workflow_scope,
        default_workflow,
        hide_builtin_workflows,
        hide_builtin_presets,
        branch_copy_uncommitted_files_default,
        ...additional
      } = preferences as Record<string, unknown>;

      const result = await settingsGrpc.updatePreferences({
        streaming_enabled: streaming_enabled as boolean | undefined,
        worktree_archive_mode: worktree_archive_mode as string | undefined,
        worktree_default_delete_directory: worktree_default_delete_directory as
          | boolean
          | undefined,
        worktree_default_delete_branch: worktree_default_delete_branch as
          | boolean
          | undefined,
        branch_copy_uncommitted_files_default: branch_copy_uncommitted_files_default as
          | boolean
          | undefined,
        default_mcp_scope: default_mcp_scope as Preferences["default_mcp_scope"] | undefined,
        default_workflow_scope: default_workflow_scope as Preferences["default_workflow_scope"] | undefined,
        default_workflow: default_workflow as string | undefined,
        hide_builtin_workflows: hide_builtin_workflows as boolean | undefined,
        hide_builtin_presets: hide_builtin_presets as boolean | undefined,
        additional: Object.fromEntries(
          Object.entries(additional).map(([k, v]) => [k, String(v)])
        ),
      });
      return { message: result.message };
    },

    setHiddenItem: async (itemType: HiddenItemType, slug: string, hidden: boolean) => {
      return settingsGrpc.setHiddenItem(itemType, slug, hidden);
    },


    getProviders: async () => {
      const providers = await settingsGrpc.getProviderStatuses();
      return providers.map((p) => ({
        provider: p.provider,
        configured: p.configured,
        hasApiKey: p.has_api_key,
        maskedKey: p.masked_key,
        displayName: p.display_name,
      }));
    },

    getProvider: async (provider: string) => {
      // Get all providers and filter - gRPC doesn't have a single-get endpoint
      const providers = await settingsGrpc.getProviderStatuses();
      const found = providers.find((p) => p.provider === provider);
      if (!found) {
        throw new Error(`Provider ${provider} not found`);
      }
      return {
        provider: found.provider,
        configured: found.configured,
        hasApiKey: found.has_api_key,
        maskedKey: found.masked_key,
        displayName: found.display_name,
      };
    },

    updateProvider: async (provider: string, apiKey: string) => {
      const result = await settingsGrpc.updateProviderAPIKey(provider, apiKey);
      return { message: result.message };
    },

    deleteProvider: async (provider: string) => {
      // Delete by setting empty API key
      const result = await settingsGrpc.updateProviderAPIKey(provider, "");
      return { message: result.message };
    },

    validateProviderAPIKey: async (provider: string, apiKey: string) => {
      return settingsGrpc.validateProviderAPIKey(provider, apiKey);
    },

    completeCodexOAuth: async (code: string, codeVerifier: string, redirectURI: string) => {
      return settingsGrpc.completeCodexOAuth(code, codeVerifier, redirectURI);
    },

    getPrivacySettings: async () => {
      return settingsGrpc.getPrivacySettings();
    },

    updatePrivacySettings: async (settings: {
      analytics_enabled?: boolean;
      crash_reporting_enabled?: boolean;
    }) => {
      return settingsGrpc.updatePrivacySettings(settings);
    },

    trackPageVisited: (pageName: string, previousPage?: string) => {
      return settingsGrpc.trackPageVisited(pageName, previousPage);
    },


    getPrompts: async () => {
      return settingsGrpc.getPrompts();
    },

    savePrompts: async (
      prompts: Array<{
        id: string;
        title: string;
        content: string;
        default?: boolean;
        hotkey?: string;
        category?: string;
      }>
    ) => {
      return settingsGrpc.savePrompts(prompts);
    },


    createSetting: async (
      key: string,
      value: string,
      valueType?: string,
      projectId?: string
    ) => {
      return settingsGrpc.createSetting(key, value, valueType, projectId);
    },

    listSettings: async (projectId?: string) => {
      return settingsGrpc.listSettings(projectId);
    },

    getSetting: async (key: string, projectId?: string) => {
      return settingsGrpc.getSetting(key, projectId);
    },

    updateSetting: async (
      key: string,
      value?: string,
      valueType?: string,
      projectId?: string
    ) => {
      return settingsGrpc.updateSetting(key, value, valueType, projectId);
    },

    deleteSetting: async (key: string, projectId?: string) => {
      return settingsGrpc.deleteSetting(key, projectId);
    },
  },

  git: {
    getBranches: async (projectId: string) => {
      const branches = await projectGrpc.getGitBranches(projectId);
      return { branches };
    },

    initGitRepository: async (
      projectId: string,
      options: {
        initial_branch?: string;
        gitignore_patterns?: string[];
        initial_commit?: boolean;
      }
    ) => {
      const result = await projectGrpc.initializeGitRepo(
        projectId,
        options.initial_branch,
        options.gitignore_patterns,
        options.initial_commit
      );
      return {
        message: result.message,
        project_id: result.project_id,
        is_git_repo: result.is_git_repo,
        default_branch: result.default_branch,
      };
    },
  },

  mcp: {
    listServers: (projectId: string) => mcpGrpc.listServers(projectId),
    getServer: (projectId: string, name: string) =>
      mcpGrpc.getServer(projectId, name),
    installServer: (
      projectId: string,
      name: string,
      config: {
        command: string;
        args?: string[];
        env?: string[];
        type: string;
        url?: string;
      }
    ) => mcpGrpc.installServer(projectId, name, config),
    uninstallServer: (projectId: string, name: string) =>
      mcpGrpc.uninstallServer(projectId, name),
    restartServer: (projectId: string, name: string) =>
      mcpGrpc.restartServer(projectId, name),
    listRecommended: (projectId: string) => mcpGrpc.listRecommended(projectId),
    updateServerConfig: (
      projectId: string,
      name: string,
      env: Record<string, string>
    ) => mcpGrpc.updateServerConfig(projectId, name, env),
  },

  // Background processes - use backgroundGrpc directly from background-grpc.ts
};