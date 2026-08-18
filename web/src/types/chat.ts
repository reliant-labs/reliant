// TypeScript types for chat - Derived from proto message types
//
// Types use proto enum types directly (no string conversion layer).
// The gRPC adapter layer passes proto values through as-is.
//
// We use the actual proto message types (not MessageInitShape) since these
// represent data received from the server where required fields are present.

import type {
  Chat as ProtoChat,
  Message as ProtoMessage,
  ContentBlock as ProtoContentBlock,
  Attachment as ProtoAttachment,
  MatchedToolResult as ProtoMatchedToolResult,
  ArchivedChat as ProtoArchivedChat,
  WorkflowExecution as ProtoWorkflowExecution,
  StepExecution as ProtoStepExecution,
} from "../gen/reliant/v1/chat_pb";


// Re-export proto enums for convenient use throughout the app
export {
  ChatState,
  WorkflowState,
  WorkflowStopReason,
  MessageRole,
  StreamingState,
  DisplayStyle,
  ContentBlockType,
} from "../gen/reliant/v1/chat_pb";



// =============================================================================
// CORE CHAT TYPES - from chat.proto
// $typeName is made optional so objects can be constructed without it.
// Enum fields use proto enum types directly.
// =============================================================================

/** Chat with $typeName optional and extra client-side fields */
export type Chat = Omit<ProtoChat, '$typeName'> & {
  $typeName?: string;
  // Flattened from ArchivedChat metadata in client.ts listArchived
  worktreeName?: string;
  worktreeDeletedAt?: string;
};

/** Message with $typeName optional and contentBlocks using our ContentBlock type */
export type Message = Omit<ProtoMessage, 'contentBlocks' | '$typeName'> & {
  $typeName?: string;
  contentBlocks: ContentBlock[];
};

/** ContentBlock with matchedResult using our MatchedToolResult type */
export type ContentBlock = Omit<ProtoContentBlock, 'matchedResult' | '$typeName'> & {
  $typeName?: string;
  matchedResult?: MatchedToolResult;
};

export type Attachment = ProtoAttachment;
/** MatchedToolResult with $typeName made optional for inline construction */
export type MatchedToolResult = Omit<ProtoMatchedToolResult, '$typeName'> & {
  $typeName?: string;
};

/** ArchivedChat with chat field using our Chat type */
export type ArchivedChat = Omit<ProtoArchivedChat, 'chat' | '$typeName'> & {
  $typeName?: string;
  chat?: Chat;
};


/** WorkflowExecution with recursive children/steps using our types */
export type WorkflowExecutionData = Omit<ProtoWorkflowExecution, 'children' | 'steps' | '$typeName'> & {
  $typeName?: string;
  children: WorkflowExecutionData[];
  steps: StepExecutionData[];
};

/** StepExecution with $typeName optional */
export type StepExecutionData = Omit<ProtoStepExecution, '$typeName'> & {
  $typeName?: string;
};

