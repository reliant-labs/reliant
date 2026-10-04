import type { WorkflowExecutionView } from "../gen/reliant/v1/chat_pb";

export const chatDetailKeys = {
  all: ["chatDetails"] as const,
  // Prefix shared by every view of one chat's execution tree, so a refetch
  // pulse can invalidate all of them with one call.
  workflowExecutions: (chatId: string) =>
    [...chatDetailKeys.all, "workflowExecutions", chatId] as const,
  // One cache entry per view: a BASIC tree carries a handful of steps and a
  // FULL one every step, so they are different data and must never be served
  // in place of each other.
  workflowExecutionsView: (chatId: string, view: WorkflowExecutionView) =>
    [...chatDetailKeys.workflowExecutions(chatId), view] as const,
  plans: (chatId: string) =>
    [...chatDetailKeys.all, "plans", chatId] as const,
  branches: (chatId: string) =>
    [...chatDetailKeys.all, "branches", chatId] as const,
  threadWorkflowInputs: (chatId: string, threadId: string) =>
    [...chatDetailKeys.all, "threadInputs", chatId, threadId] as const,
};
