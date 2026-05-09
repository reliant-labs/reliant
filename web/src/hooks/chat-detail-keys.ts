export const chatDetailKeys = {
  all: ["chatDetails"] as const,
  workflowExecutions: (chatId: string) =>
    [...chatDetailKeys.all, "workflowExecutions", chatId] as const,
  plans: (chatId: string) =>
    [...chatDetailKeys.all, "plans", chatId] as const,
  branches: (chatId: string) =>
    [...chatDetailKeys.all, "branches", chatId] as const,
  threadWorkflowInputs: (chatId: string, threadId: string) =>
    [...chatDetailKeys.all, "threadInputs", chatId, threadId] as const,
};
