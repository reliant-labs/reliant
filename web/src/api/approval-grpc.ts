// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import type { Approval as ProtoApproval } from "../gen/reliant/v1/approval_pb";
import { ApprovalStatus } from "../gen/reliant/v1/approval_pb";

export { ApprovalStatus };
import {
  ListApprovalsByChatRequestSchema,
  ApproveRequestSchema,
  DenyRequestSchema,
  BatchApproveRequestSchema,
  BatchDenyRequestSchema,
} from "../gen/reliant/v1/approval_pb";

export interface ToolApprovalRequest {
  id: string;
  chat_id: string;
  content_block_id: string;
  message_id?: string;
  tool_call_id?: string;
  tool_name?: string;
  description?: string;
  status: ApprovalStatus;
  created_at: string;
  responded_at?: string;
  responded_by?: string;
  denial_reason?: string;
  action_taken?: string;  // Which action button was clicked
}

// Convert proto Approval to frontend ToolApprovalRequest
function protoToFrontend(proto: ProtoApproval): ToolApprovalRequest {
  return {
    id: proto.id,
    chat_id: proto.chatId,
    content_block_id: proto.entityId, // entity_id maps to content_block_id for tool approvals
    message_id: proto.messageId,
    tool_call_id: proto.toolCallId,
    tool_name: proto.toolName,
    description: proto.description,
    status: proto.status,
    created_at: proto.createdAt,
    responded_at: proto.resolvedAt,
    denial_reason: proto.denialReason,
    action_taken: proto.actionTaken,
  };
}

export const approvalGrpc = {
  // List all pending approvals for a chat
  async listByChat(chatId: string): Promise<ToolApprovalRequest[]> {
    const client = grpcClient.approval();
    const request = create(ListApprovalsByChatRequestSchema, { chatId });
    const response = await client.listApprovalsByChat(request);
    return response.approvals.map(protoToFrontend);
  },

  // Approve a single approval request
  async approve(requestId: string, actionTaken?: string): Promise<{ success: boolean; message: string }> {
    const client = grpcClient.approval();
    const request = create(ApproveRequestSchema, { requestId, actionTaken });
    const response = await client.approve(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // Deny a single approval request
  async deny(requestId: string, denialReason?: string, actionTaken?: string): Promise<{ success: boolean; message: string }> {
    const client = grpcClient.approval();
    const request = create(DenyRequestSchema, {
      requestId,
      denialReason,
      actionTaken,
    });
    const response = await client.deny(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // Batch approve multiple approval requests
  async batchApprove(requestIds: string[], actionTaken?: string): Promise<{ success: boolean; approved: number; message: string }> {
    const client = grpcClient.approval();
    const request = create(BatchApproveRequestSchema, { requestIds, actionTaken });
    const response = await client.batchApprove(request);
    return {
      success: response.success,
      approved: response.approved,
      message: response.message,
    };
  },

  // Batch deny multiple approval requests
  async batchDeny(requestIds: string[], denialReason?: string, actionTaken?: string): Promise<{ success: boolean; denied: number; message: string }> {
    const client = grpcClient.approval();
    const request = create(BatchDenyRequestSchema, {
      requestIds,
      denialReason,
      actionTaken,
    });
    const response = await client.batchDeny(request);
    return {
      success: response.success,
      denied: response.denied,
      message: response.message,
    };
  },
};