import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import type { ToolApprovalRequest } from "../api/approval-grpc";
import { ApprovalStatus } from "../api/approval-grpc";
import { questionGrpc, type QuestionInfo } from "../api/question-grpc";
import { toolCallGrpc } from "../api/tool-call-grpc";

// --- Query key factories ---

export const approvalKeys = {
  all: ["approvals"] as const,
  list: (chatId: string) => [...approvalKeys.all, "list", chatId] as const,
  pending: (chatId: string) =>
    [...approvalKeys.all, "pending", chatId] as const,
};

export const questionKeys = {
  all: ["questions"] as const,
  pending: (chatId: string) =>
    [...questionKeys.all, "pending", chatId] as const,
};

// --- Query hooks ---

export function useApprovals(chatId?: string) {
  return useQuery<ToolApprovalRequest[]>({
    queryKey: approvalKeys.list(chatId!),
    queryFn: () => api.approvals.listByChat(chatId!),
    enabled: !!chatId,
  });
}

export function usePendingApprovals(chatId?: string) {
  return useQuery<ToolApprovalRequest[], Error, ToolApprovalRequest[]>({
    queryKey: approvalKeys.list(chatId!),
    queryFn: () => api.approvals.listByChat(chatId!),
    enabled: !!chatId,
    select: (data) =>
      data.filter((a) => a.status === ApprovalStatus.PENDING),
  });
}

export function usePendingQuestion(chatId?: string) {
  return useQuery<QuestionInfo | null>({
    queryKey: questionKeys.pending(chatId!),
    queryFn: () => questionGrpc.getPendingQuestion(chatId!),
    enabled: !!chatId,
  });
}

// --- Mutation hooks ---

export function useApproveToolRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      requestId,
      actionTaken,
    }: {
      requestId: string;
      actionTaken?: string;
    }) => api.approvals.approve(requestId, actionTaken),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: approvalKeys.all });
    },
  });
}

export function useDenyToolRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      requestId,
      denialReason,
      actionTaken,
    }: {
      requestId: string;
      denialReason?: string;
      actionTaken?: string;
    }) => api.approvals.deny(requestId, denialReason, actionTaken),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: approvalKeys.all });
    },
  });
}

export function useBatchApprove() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      requestIds,
      actionTaken,
    }: {
      requestIds: string[];
      actionTaken?: string;
    }) => api.approvals.batchApprove(requestIds, actionTaken),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: approvalKeys.all });
    },
  });
}

export function useBatchDeny() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      requestIds,
      denialReason,
      actionTaken,
    }: {
      requestIds: string[];
      denialReason?: string;
      actionTaken?: string;
    }) => api.approvals.batchDeny(requestIds, denialReason, actionTaken),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: approvalKeys.all });
    },
  });
}

export function useResolveQuestion() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      questionId,
      action,
      responseData,
    }: {
      questionId: string;
      action: string;
      responseData?: string;
    }) => questionGrpc.resolveQuestion(questionId, action, responseData),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: questionKeys.all });
    },
  });
}

export function useCancelToolCall() {
  return useMutation({
    mutationFn: (toolCallId: string) => toolCallGrpc.cancel(toolCallId),
  });
}

export function useConvertToBackground() {
  return useMutation({
    mutationFn: (toolCallId: string) =>
      toolCallGrpc.convertToBackground(toolCallId),
  });
}
