// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";

// Re-export types from generated code
export type {
  CancelToolCallRequest,
  CancelToolCallResponse,
  ConvertToBackgroundRequest,
  ConvertToBackgroundResponse,
} from "../gen/reliant/v1/tool_call_pb";

// Tool call response types for API compatibility
export interface ToolCallCancelResponse {
  status: string;
  message: string;
  tool_call_id: string;
  session_id: string;
  chat_id: string;
}

export interface ToolCallConvertToBackgroundResponse {
  status: string;
  message: string;
  process_id: string;
  tool_call_id: string;
  session_id: string;
  chat_id: string;
}

// Tool call gRPC API wrapper
export const toolCallGrpc = {
  /**
   * Cancel a tool call
   */
  async cancel(toolCallId: string): Promise<ToolCallCancelResponse> {
    const client = grpcClient.toolCall();
    const response = await client.cancelToolCall({ toolCallId });
    return {
      status: response.success ? "success" : "error",
      message: response.message,
      tool_call_id: response.toolCallId,
      session_id: response.sessionId,
      chat_id: response.chatId,
    };
  },

  /**
   * Convert a tool call to background execution
   */
  async convertToBackground(
    toolCallId: string
  ): Promise<ToolCallConvertToBackgroundResponse> {
    const client = grpcClient.toolCall();
    const response = await client.convertToBackground({ toolCallId });
    return {
      status: response.success ? "success" : "error",
      message: response.message,
      process_id: response.processId,
      tool_call_id: response.toolCallId,
      session_id: response.sessionId,
      chat_id: response.chatId,
    };
  },
};
