import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import { chatGrpc } from "../api/chat-grpc";
import { chatDetailKeys } from "./chat-detail-keys";

// Re-export key factory for consumers
export { chatDetailKeys };

// ── Query hooks ─────────────────────────────────────────────────────────────

export function useWorkflowExecutions(chatId?: string) {
  return useQuery({
    queryKey: chatDetailKeys.workflowExecutions(chatId!),
    queryFn: () => chatGrpc.getWorkflowExecutions(chatId!),
    enabled: !!chatId,
  });
}

export function useChatPlans(chatId?: string) {
  return useQuery({
    queryKey: chatDetailKeys.plans(chatId!),
    queryFn: () => chatGrpc.listPlans(chatId!),
    enabled: !!chatId,
  });
}

export function useChatBranches(chatId?: string) {
  return useQuery({
    queryKey: chatDetailKeys.branches(chatId!),
    queryFn: () => chatGrpc.listBranches(chatId!),
    enabled: !!chatId,
  });
}

export function useThreadWorkflowInputs(chatId?: string, threadId?: string) {
  return useQuery({
    queryKey: chatDetailKeys.threadWorkflowInputs(chatId!, threadId!),
    queryFn: () => chatGrpc.getThreadWorkflowInputs(chatId!, threadId!),
    enabled: !!chatId && !!threadId,
  });
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

export function useUpdateWorkflowParams() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      chatId,
      params,
      threadId,
    }: {
      chatId: string;
      params: Record<string, unknown>;
      threadId?: string;
    }) => api.chatsV2.updateWorkflowParams(chatId, params, threadId),
    onSuccess: (_data, { chatId, threadId }) => {
      if (threadId) {
        queryClient.invalidateQueries({
          queryKey: chatDetailKeys.threadWorkflowInputs(chatId, threadId),
        });
      }
    },
  });
}
