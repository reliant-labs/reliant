// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import {
  ResolveYieldRequestSchema,
  GetPendingYieldRequestSchema,
} from "../gen/reliant/v1/yield_pb";
import { YieldStatus } from "../gen/reliant/v1/yield_pb";

export { YieldStatus };

export interface YieldInfo {
  yield_id: string;
  chat_id: string;
  workflow_id: string;
  step_id: string;
  status: YieldStatus;
  created_at: string;
}

export const yieldGrpc = {
  async resolveYield(
    yieldId: string,
    action: string,
  ): Promise<{ success: boolean }> {
    const client = grpcClient.yield();
    const request = create(ResolveYieldRequestSchema, { yieldId, action });
    const response = await client.resolveYield(request);
    return { success: response.success };
  },

  async getPendingYield(chatId: string): Promise<YieldInfo | null> {
    const client = grpcClient.yield();
    const request = create(GetPendingYieldRequestSchema, { chatId });
    const response = await client.getPendingYield(request);
    if (!response.yield) return null;
    return {
      yield_id: response.yield.yieldId,
      chat_id: response.yield.chatId,
      workflow_id: response.yield.workflowId,
      step_id: response.yield.stepId,
      status: response.yield.status,
      created_at: response.yield.createdAt,
    };
  },
};
