import {
  useQuery,
  useMutation,
  useInfiniteQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api/client";
import type { Message } from "../types/chat";
import { getEventBus } from "../lib/events";
import { chatKeys } from "./chat-queries";

// ── Query key factory ───────────────────────────────────────────────────────

export const messageKeys = {
  all: ["messages"] as const,
  list: (chatId: string) => [...messageKeys.all, "list", chatId] as const,
  infinite: (chatId: string) =>
    [...messageKeys.all, "infinite", chatId] as const,
};

// ── Query hooks ─────────────────────────────────────────────────────────────

export function useMessages(
  chatId?: string,
  options?: { recent?: number }
) {
  return useQuery({
    queryKey: messageKeys.list(chatId!),
    queryFn: () =>
      api.chatsV2.listMessages(chatId!, { recent: options?.recent }),
    enabled: !!chatId,
  });
}

export function useInfiniteMessages(chatId?: string) {
  return useInfiniteQuery({
    queryKey: messageKeys.infinite(chatId!),
    queryFn: ({ pageParam }) =>
      api.chatsV2.listMessages(chatId!, {
        beforeOrdinal: pageParam,
      }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (lastPage) =>
      lastPage.hasMore ? lastPage.oldestOrdinal : undefined,
    enabled: !!chatId,
  });
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

export function useSendMessage() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      chatId,
      content,
      attachments,
      options,
    }: {
      chatId: string;
      content: string;
      attachments?: string[];
      options?: {
        workflow?: string | null;
        mode?: string;
        temperature?: number;
        max_tokens?: number;
        workflow_params?: Record<string, unknown>;
        target_thread?: string;
        selected_presets?: Record<string, string>;
        systemMessages?: Array<{ content: string }>;
        discuss?: boolean;
      };
    }) => api.chatsV2.sendMessage(chatId, content, attachments, options),
    onSuccess: (data) => {
      queryClient.invalidateQueries({
        queryKey: messageKeys.list(data.chatId),
      });
      try {
        getEventBus().emit("stream:started", { chatId: data.chatId });
      } catch {
        // Event bus may not be initialized — non-fatal
      }
    },
  });
}

export function useCancelChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.cancel(chatId),
    onSuccess: (_data, chatId) => {
      queryClient.invalidateQueries({ queryKey: chatKeys.all });
    },
  });
}

export function usePauseChat() {
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.pause(chatId),
  });
}

export function useResumeChat() {
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.resume(chatId),
  });
}

export function useBranchChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      chatId,
      messageId,
      title,
      worktreeId,
    }: {
      chatId: string;
      messageId?: string;
      title?: string;
      worktreeId?: string;
    }) => api.chatsV2.branch(chatId, { messageId, title, worktreeId }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
    },
  });
}

export function useCompactChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      chatId,
      threadId,
    }: {
      chatId: string;
      threadId: string;
    }) => api.chatsV2.compact(chatId, threadId),
    onSuccess: (_data, { chatId }) => {
      queryClient.invalidateQueries({
        queryKey: messageKeys.list(chatId),
      });
    },
  });
}

export function useDismissChat() {
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.dismiss(chatId),
  });
}

export function useMarkUnread() {
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.markUnread(chatId),
  });
}
