// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { singleflight } from "../lib/singleflight";
import { create } from "@bufbuild/protobuf";
import { jsToProtoValue, protoValueToJs, type ProtoValue } from "./proto-utils";
import {
  CreateChatRequestSchema,
  ListChatsRequestSchema,
  GetChatRequestSchema,
  UpdateChatRequestSchema,
  DeleteChatRequestSchema,
  SearchChatsRequestSchema,
  ListArchivedChatsRequestSchema,
  SendMessageRequestSchema,
  ListMessagesRequestSchema,
  UpdateChatStateRequestSchema,
  CancelChatRequestSchema,
  PauseChatRequestSchema,
  ResumeChatRequestSchema,
  DismissChatRequestSchema,
  MarkUnreadChatRequestSchema,
  CompactChatRequestSchema,
  BranchChatRequestSchema,
  ListBranchesRequestSchema,
  UpdateWorkflowParamsRequestSchema,
  GetChatUpdatesRequestSchema,
  ListChatPlansRequestSchema,
  WorkspaceBranchContextSchema,
  GetWorkflowExecutionsRequestSchema,
  GetThreadWorkflowInputsRequestSchema,
  ForceYieldThreadRequestSchema,
  ChatState,
  MessageRole,
  DisplayStyle,
} from "../gen/reliant/v1/chat_pb";
import { PlanStatus } from "../gen/reliant/v1/common_pb";

export type {
  Chat,
  ArchivedChat,
  ContentBlock,
  MatchedToolResult,
  Attachment,
  Message,
  WorkflowExecutionData,
  StepExecutionData,
} from "../types/chat";

// Re-export proto enums for consumers
export { ChatState, ChatWorkflowStatus, MessageRole, StreamingState, DisplayStyle, ContentBlockType } from "../types/chat";

import type {
  Chat,
  ArchivedChat,
  Message,
  ContentBlock,
  WorkflowExecutionData,
  StepExecutionData,
} from "../types/chat";

import type {
  Chat as ProtoChat,
  Message as ProtoMessage,
  ContentBlock as ProtoContentBlock,
  WorkflowExecution as ProtoWorkflowExecution,
  StepExecution as ProtoStepExecution,
} from "../gen/reliant/v1/chat_pb";


// ============================================
// Proto → Frontend Converters
// ============================================
// Proto types have $typeName as a required discriminant. Frontend types
// make $typeName optional, so proto objects are structurally assignable.
// These converters destructure away $typeName and recursively convert
// nested proto types.

function convertProtoContentBlock(b: ProtoContentBlock): ContentBlock {
  const { $typeName: _, matchedResult, ...rest } = b;
  return {
    ...rest,
    matchedResult: matchedResult
      ? (() => { const { $typeName: _t, ...mr } = matchedResult; return mr; })()
      : undefined,
  };
}

function convertProtoMessage(m: ProtoMessage): Message {
  const { $typeName: _, contentBlocks, ...rest } = m;
  return {
    ...rest,
    contentBlocks: contentBlocks.map(convertProtoContentBlock),
  };
}

function convertProtoStepExecution(s: ProtoStepExecution): StepExecutionData {
  const { $typeName: _, ...rest } = s;
  return rest;
}

function convertProtoWorkflowExecution(w: ProtoWorkflowExecution): WorkflowExecutionData {
  const { $typeName: _, children, steps, ...rest } = w;
  return {
    ...rest,
    steps: steps.map(convertProtoStepExecution),
    children: children.map(convertProtoWorkflowExecution),
  };
}

function convertProtoChat(proto: ProtoChat): Chat {
  const { $typeName: _, ...rest } = proto;
  return rest;
}

export interface BranchInfo {
  id: string;
  title: string;
  branched_at_ordinal: number;
  created_at: string;
  message_count: number;
  last_active: string;
}

export interface ChatUpdate {
  sequence_number: number;
  update_type: string;
  entity_id: string;
  data: string; // JSON string
  created_at: string;
}

export interface ChatPlan {
  id: string;
  chat_id: string;
  title: string;
  description?: string;
  status: PlanStatus;
  created_at: string;
  updated_at: string;
  completed_at?: string;
}

// InputMessage for CreateChat/SendMessage requests
// Supports user and system messages.
export interface InputMessage {
  role: MessageRole;
  content: string;
  display_style?: DisplayStyle;
}

export interface CreateChatOptions {
  project_id: string;
  messages: InputMessage[];  // At least one user message required
  title?: string;
  worktree_id?: string;
  workflow?: string;  // Optional - defaults to user's preference or builtin://agent
  temperature?: number;
  max_tokens?: number;
  mode?: string;
  attachments?: string[];
  workflow_params?: Record<string, unknown>;
  selected_presets?: Record<string, string>; // Preset selections per target
}

// Chat updates only affect title and worktree_id
// Model/temperature/max_tokens are per-message workflow params
export interface UpdateChatOptions {
  title?: string;
  worktree_id?: string;
  workflow_name?: string; // Only changeable when root workflow is pending (chat hasn't started)
}

export interface SendMessageOptions {
  messages: InputMessage[];  // At least one user message required
  attachments?: string[];
  workflow?: string;
  mode?: string;
  temperature?: number;
  max_tokens?: number;
  workflow_params?: Record<string, unknown>;
  target_thread?: string;
  selected_presets?: Record<string, string>; // Update preset selections
  yield_id?: string; // If set, resolves a pending yield
}

export interface ListMessagesOptions {
  recent?: number;
  before_ordinal?: number;
}

function hasDottedWorkflowParamKey(workflowParams: Record<string, unknown>): boolean {
  return Object.keys(workflowParams).some((key) => key.includes("."));
}

export function buildWorkflowParamsPayload(
  workflowParams?: Record<string, unknown>,
): Record<string, ProtoValue> {
  if (!workflowParams) {
    return {};
  }

  if (hasDottedWorkflowParamKey(workflowParams)) {
    throw new Error(
      "workflow_params must use nested object keys. Dotted keys are no longer supported.",
    );
  }

  const protoParams: Record<string, ProtoValue> = {};
  for (const [key, value] of Object.entries(workflowParams)) {
    protoParams[key] = jsToProtoValue(value);
  }

  return protoParams;
}

export const chatGrpc = {
  // Create a new chat and start its workflow
  async create(options: CreateChatOptions): Promise<{
    chat: Chat;
    workflow_id: string;
    run_id: string;
    draft_id?: string;  // Draft ID for workflow builder chats (when a new draft was created)
  }> {
    const client = grpcClient.chat();
    const workflowParams = buildWorkflowParamsPayload(options.workflow_params);

    const request = create(CreateChatRequestSchema, {
      projectId: options.project_id,
      messages: options.messages.map(m => ({ role: m.role, content: m.content, displayStyle: m.display_style })),
      title: options.title,
      worktreeId: options.worktree_id,
      workflow: options.workflow,
      temperature: options.temperature,
      maxTokens: options.max_tokens,
      mode: options.mode,
      attachments: options.attachments || [],
      workflowParams,
      selectedPresets: options.selected_presets || {},
    });
    const response = await client.createChat(request);
    if (!response.chat) throw new Error("No chat in response");
    return {
      chat: convertProtoChat(response.chat),
      workflow_id: response.workflowId,
      run_id: response.runId,
      draft_id: response.draftId,
    };
  },

  // List all non-archived chats for a project
  async list(
    projectId: string,
    limit?: number
  ): Promise<{
    chats: Chat[];
    total: number;
    lastUserUpdateSequence: number;
  }> {
    const client = grpcClient.chat();
    const request = create(ListChatsRequestSchema, {
      projectId,
      limit: limit,
    });
    const response = await client.listChats(request);
    return {
      chats: response.chats.map(convertProtoChat),
      total: response.total,
      lastUserUpdateSequence: Number(response.lastUserUpdateSequence),
    };
  },

  // Get a specific chat by ID
  async get(chatId: string): Promise<Chat> {
    const client = grpcClient.chat();
    const request = create(GetChatRequestSchema, { chatId });
    const response = await client.getChat(request);
    if (!response.chat) throw new Error("No chat in response");
    return convertProtoChat(response.chat);
  },

  // Update chat metadata
  // NOTE: model/temperature/max_tokens are workflow params - use UpdateWorkflowParams
  // NOTE: workflow_name only changeable when root workflow status is "pending"
  async update(chatId: string, updates: UpdateChatOptions): Promise<Chat> {
    const client = grpcClient.chat();
    const request = create(UpdateChatRequestSchema, {
      chatId,
      title: updates.title,
      worktreeId: updates.worktree_id,
      workflowName: updates.workflow_name,
    });
    const response = await client.updateChat(request);
    if (!response.chat) throw new Error("No chat in response");
    return convertProtoChat(response.chat);
  },

  // Delete (archive) a chat
  async delete(chatId: string): Promise<{
    success: boolean;
    message: string;
    permanently_deleted: boolean;
  }> {
    const client = grpcClient.chat();
    const request = create(DeleteChatRequestSchema, { chatId });
    const response = await client.deleteChat(request);
    return {
      success: response.success,
      message: response.message,
      permanently_deleted: response.permanentlyDeleted,
    };
  },

  // Search chats by title and message content
  async search(
    projectId: string,
    query: string,
    limit?: number
  ): Promise<{
    chats: Chat[];
    total: number;
  }> {
    if (!query) {
      return { chats: [], total: 0 };
    }
    const client = grpcClient.chat();
    const request = create(SearchChatsRequestSchema, {
      projectId,
      query,
      limit: limit,
    });
    const response = await client.searchChats(request);
    return {
      chats: response.chats.map(convertProtoChat),
      total: response.total,
    };
  },

  // List all archived chats with worktree info
  async listArchived(): Promise<{
    chats: ArchivedChat[];
    total: number;
  }> {
    return singleflight('listArchivedChats', async () => {
      const client = grpcClient.chat();
      const request = create(ListArchivedChatsRequestSchema, {});
      const response = await client.listArchivedChats(request);
      return {
        chats: response.chats.map(ac => {
          const { $typeName: _, chat, ...rest } = ac;
          return {
            ...rest,
            chat: chat ? convertProtoChat(chat) : undefined,
          };
        }),
        total: response.total,
      };
    });
  },

  // Send a user message and continue workflow
  async sendMessage(
    chatId: string,
    options: SendMessageOptions
  ): Promise<{
    chat_id: string;
    workflow_id: string;
    run_id: string;
    status: string;
    message_id: string;
  }> {
    const client = grpcClient.chat();
    const workflowParams = buildWorkflowParamsPayload(options.workflow_params);

    const request = create(SendMessageRequestSchema, {
      chatId,
      messages: options.messages.map(m => ({ role: m.role, content: m.content, displayStyle: m.display_style })),
      attachments: options.attachments || [],
      workflow: options.workflow,
      mode: options.mode,
      temperature: options.temperature,
      maxTokens: options.max_tokens ? BigInt(options.max_tokens) : undefined,
      workflowParams,
      targetThread: options.target_thread,
      selectedPresets: options.selected_presets || {},
      yieldId: options.yield_id,
    });
    const response = await client.sendMessage(request);
    return {
      chat_id: response.chatId,
      workflow_id: response.workflowId,
      run_id: response.runId,
      status: response.status,
      message_id: response.messageId,
    };
  },

  // List messages with content blocks
  async listMessages(
    chatId: string,
    options?: ListMessagesOptions
  ): Promise<{
    messages: Message[];
    total: number;
    count: number;
    has_more: boolean;
    oldest_ordinal: number;
  }> {
    const client = grpcClient.chat();
    const request = create(ListMessagesRequestSchema, {
      chatId,
      recent: options?.recent,
      beforeOrdinal: options?.before_ordinal
        ? BigInt(options.before_ordinal)
        : undefined,
    });
    const response = await client.listMessages(request);
    return {
      messages: response.messages.map(convertProtoMessage),
      total: response.total,
      count: response.count,
      has_more: response.hasMore,
      oldest_ordinal: Number(response.oldestOrdinal),
    };
  },

  // Change chat state (idle or archived)
  async updateState(
    chatId: string,
    state: ChatState
  ): Promise<{
    state: ChatState;
    previous_state: ChatState;
    message: string;
  }> {
    const client = grpcClient.chat();
    const request = create(UpdateChatStateRequestSchema, {
      chatId,
      state,
    });
    const response = await client.updateChatState(request);
    return {
      state: response.state,
      previous_state: response.previousState,
      message: response.message,
    };
  },

  // Cancel the running workflow
  async cancel(chatId: string): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = grpcClient.chat();
    const request = create(CancelChatRequestSchema, { chatId });
    const response = await client.cancelChat(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // Pause the running workflow
  async pause(chatId: string): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = grpcClient.chat();
    const request = create(PauseChatRequestSchema, { chatId });
    const response = await client.pauseChat(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // Resume a paused or expired workflow
  async resume(chatId: string): Promise<{
    success: boolean;
    message: string;
    workflow_id: string;
    run_id: string;
    needs_recovery: boolean;
    recovery_type: number;
  }> {
    const client = grpcClient.chat();
    const request = create(ResumeChatRequestSchema, { chatId });
    const response = await client.resumeChat(request);
    return {
      success: response.success,
      message: response.message,
      workflow_id: response.workflowId,
      run_id: response.runId,
      needs_recovery: response.needsRecovery,
      recovery_type: response.recoveryType,
    };
  },

  // Force-yield a running thread back to the parent workflow
  async forceYieldThread(chatId: string, threadId: string): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = grpcClient.chat();
    const request = create(ForceYieldThreadRequestSchema, { chatId, threadId });
    const response = await client.forceYieldThread(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // Dismiss unread state (proper gRPC call)
  async dismiss(chatId: string): Promise<{
    state: ChatState;
    changed: boolean;
    message: string;
  }> {
    const client = grpcClient.chat();
    const request = create(DismissChatRequestSchema, { chatId });
    const response = await client.dismissChat(request);
    return {
      state: response.state,
      changed: response.changed,
      message: response.message,
    };
  },

  // Mark chat as unread
  async markUnread(chatId: string): Promise<{
    state: ChatState;
    changed: boolean;
    message: string;
  }> {
    const client = grpcClient.chat();
    const request = create(MarkUnreadChatRequestSchema, { chatId });
    const response = await client.markUnreadChat(request);
    return {
      state: response.state,
      changed: response.changed,
      message: response.message,
    };
  },

  // Trigger manual context compaction
  async compact(chatId: string, threadId: string): Promise<{
    success: boolean;
    workflow_id: string;
    run_id: string;
    message: string;
  }> {
    const client = grpcClient.chat();
    const request = create(CompactChatRequestSchema, { chatId, threadId });
    const response = await client.compactChat(request);
    return {
      success: true, // CompactChatResponse doesn't have success field, assume success if no error
      workflow_id: response.workflowId,
      run_id: response.runId,
      message: "", // CompactChatResponse doesn't have message field
    };
  },

  // Create a branch from a specific message
  async branch(
    chatId: string,
    options: {
      messageId: string; // Message ID to branch from (unique, unambiguous)
      title?: string;
      worktreeId?: string;
      workspaceContext?: {
        sourceWorktreeId?: string;
        filesCopied?: string[];
        copyFilesEnabled?: boolean;
      };
    }
  ): Promise<{
    chat: Chat;
  }> {
    const client = grpcClient.chat();

    if (!options.messageId) {
      throw new Error("messageId is required for branching");
    }

    // Build workspace context if provided
    const workspaceContext = options.workspaceContext
      ? create(WorkspaceBranchContextSchema, {
          sourceWorktreeId: options.workspaceContext.sourceWorktreeId,
          filesCopied: options.workspaceContext.filesCopied || [],
          copyFilesEnabled: options.workspaceContext.copyFilesEnabled ?? false,
        })
      : undefined;

    const request = create(BranchChatRequestSchema, {
      chatId,
      messageId: options.messageId,
      title: options.title,
      worktreeId: options.worktreeId,
      workspaceContext,
    });
    const response = await client.branchChat(request);
    if (!response.chat) throw new Error("No chat in response");
    return {
      chat: convertProtoChat(response.chat),
    };
  },

  // List branches of a chat
  async listBranches(chatId: string): Promise<{
    branches: BranchInfo[];
    total: number;
  }> {
    const client = grpcClient.chat();
    const request = create(ListBranchesRequestSchema, { chatId });
    const response = await client.listBranches(request);
    return {
      branches: response.branches.map((b) => ({
        id: b.id,
        title: b.title,
        branched_at_ordinal: Number(b.branchedAtOrdinal ?? 0),
        created_at: b.createdAt,
        message_count: 0, // Not available in proto, would need separate query
        last_active: b.lastActive,
      })),
      total: response.total,
    };
  },

  // Update workflow parameters for a running chat
  // This is the generic API for updating mode and other workflow params
  async updateWorkflowParams(
    chatId: string,
    params: Record<string, unknown>,
    threadId?: string
  ): Promise<{
    success: boolean;
    chat_id: string;
    message: string;
  }> {
    const client = grpcClient.chat();
    const protoParams = buildWorkflowParamsPayload(params);

    const request = create(UpdateWorkflowParamsRequestSchema, {
      chatId,
      params: protoParams,
      threadId: threadId || undefined,
    });
    const response = await client.updateWorkflowParams(request);
    return {
      success: response.success,
      chat_id: chatId,
      message: response.message,
    };
  },

  // Fetch chat updates since a sequence number
  async getUpdates(
    chatId: string,
    sinceSeq: number = 0
  ): Promise<{
    updates: ChatUpdate[];
    total: number;
    latest_sequence: number;
  }> {
    const client = grpcClient.chat();
    const request = create(GetChatUpdatesRequestSchema, {
      chatId,
      sinceSeq: BigInt(sinceSeq),
    });
    const response = await client.getChatUpdates(request);
    return {
      updates: response.updates.map((u) => ({
        sequence_number: Number(u.sequenceNumber),
        update_type: u.updateType,
        entity_id: u.entityId,
        data: u.data,
        created_at: u.createdAt,
      })),
      total: response.total,
      latest_sequence: Number(response.latestSequence),
    };
  },

  // List plans associated with a chat
  async listPlans(chatId: string): Promise<{
    plans: ChatPlan[];
    total: number;
  }> {
    const client = grpcClient.chat();
    const request = create(ListChatPlansRequestSchema, { chatId });
    const response = await client.listChatPlans(request);
    return {
      plans: response.plans.map((p) => ({
        id: p.id,
        chat_id: p.chatId,
        title: p.title,
        description: p.description || undefined,
        status: p.status,
        created_at: p.createdAt,
        updated_at: p.updatedAt,
        completed_at: p.completedAt || undefined,
      })),
      total: response.total,
    };
  },

  // ============================================
  // Convenience aliases
  // ============================================

  // Archive a chat (alias for updateState with 'archived')
  async archive(chatId: string): Promise<{ message: string }> {
    const result = await this.updateState(chatId, ChatState.ARCHIVED);
    return { message: result.message };
  },

  // Unarchive a chat (change from archived to idle)
  async unarchive(chatId: string): Promise<{
    message: string;
    worktree_restored: boolean;
  }> {
    const result = await this.updateState(chatId, ChatState.IDLE);
    return {
      message: result.message,
      worktree_restored: false, // Backend handles this automatically
    };
  },

  // ============================================
  // Workflow Execution
  // ============================================

  // Get workflow execution tree for a chat
  // Returns the latest root workflow (for backwards compat) and all root workflows
  async getWorkflowExecutions(
    chatId: string
  ): Promise<{ latest: WorkflowExecutionData | null; all: WorkflowExecutionData[] }> {
    const client = grpcClient.chat();
    const request = create(GetWorkflowExecutionsRequestSchema, { chatId });
    const response = await client.getWorkflowExecutions(request);

    const latest = response.rootWorkflow
      ? convertProtoWorkflowExecution(response.rootWorkflow)
      : null;
    
    const all = (response.allRootWorkflows || []).map(convertProtoWorkflowExecution);

    return { latest, all };
  },

  async getThreadWorkflowInputs(
    chatId: string,
    threadId: string
  ): Promise<{ workflowName: string; inputs: Record<string, unknown>; isRunning: boolean }> {
    const client = grpcClient.chat();
    const request = create(GetThreadWorkflowInputsRequestSchema, { chatId, threadId });
    const response = await client.getThreadWorkflowInputs(request);

    // Convert protobuf Value map to plain JS using shared converter
    const inputs: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(response.inputs)) {
      inputs[key] = protoValueToJs(value as any);
    }

    return {
      workflowName: response.workflowName,
      inputs,
      isRunning: response.isRunning,
    };
  },
};