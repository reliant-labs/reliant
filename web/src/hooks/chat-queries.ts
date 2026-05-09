import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, type Chat } from "../api/client";
import { getEventBus } from "../lib/events";
import type { CreateChatRequest, UpdateChatRequest } from "../types/api";

// ── Query key factory ───────────────────────────────────────────────────────

export const chatKeys = {
  all: ["chats"] as const,
  lists: () => [...chatKeys.all, "list"] as const,
  list: (projectId?: string) => [...chatKeys.lists(), projectId] as const,
  details: () => [...chatKeys.all, "detail"] as const,
  detail: (chatId?: string) => [...chatKeys.details(), chatId] as const,
  archived: () => [...chatKeys.all, "archived"] as const,
  search: (projectId: string, query: string) =>
    [...chatKeys.all, "search", projectId, query] as const,
};

// ── Query hooks ─────────────────────────────────────────────────────────────

export function useChatList(projectId?: string) {
  return useQuery({
    queryKey: chatKeys.list(projectId),
    queryFn: () => api.chatsV2.list(projectId),
    enabled: !!projectId,
    select: (data) => data.chats,
  });
}

export function useChatListWithSequence(projectId?: string) {
  return useQuery({
    queryKey: chatKeys.list(projectId),
    queryFn: () => api.chatsV2.list(projectId),
    enabled: !!projectId,
  });
}

export function useChat(chatId?: string) {
  return useQuery({
    queryKey: chatKeys.detail(chatId),
    queryFn: () => api.chatsV2.get(chatId!),
    enabled: !!chatId,
  });
}

export function useArchivedChats() {
  return useQuery({
    queryKey: chatKeys.archived(),
    queryFn: () => api.chatsV2.listArchived(),
  });
}

export function useSearchChats(projectId: string, query: string) {
  return useQuery({
    queryKey: chatKeys.search(projectId, query),
    queryFn: () => api.chatsV2.search(projectId, query),
    enabled: !!projectId && !!query,
  });
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

export function useCreateChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateChatRequest) => api.chatsV2.create(request),
    onSuccess: (chat) => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      try {
        getEventBus().emit("chat:created", { chatId: (chat as Chat).id });
      } catch {
        // Event bus may not be initialized — non-fatal
      }
    },
  });
}

export function useDeleteChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.delete(chatId),
    onSuccess: (_data, chatId) => {
      queryClient.invalidateQueries({ queryKey: chatKeys.all });
      try {
        getEventBus().emit("chat:deleted", { chatId });
      } catch {
        // Event bus may not be initialized — non-fatal
      }
    },
  });
}

export function useRenameChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ chatId, title }: { chatId: string; title: string }) =>
      api.chatsV2.update(chatId, { title } as UpdateChatRequest),
    onSuccess: (_data, { chatId, title }) => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatKeys.detail(chatId) });
      try {
        getEventBus().emit("chat:titleChanged", { chatId, title });
      } catch {
        // Event bus may not be initialized — non-fatal
      }
    },
  });
}

export function useArchiveChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.archive(chatId),
    onSuccess: (_data, chatId) => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatKeys.archived() });
      try {
        getEventBus().emit("chat:archived", { chatId });
      } catch {
        // Event bus may not be initialized — non-fatal
      }
    },
  });
}

export function useUnarchiveChat() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (chatId: string) => api.chatsV2.unarchive(chatId),
    onSuccess: (_data, chatId) => {
      queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
      queryClient.invalidateQueries({ queryKey: chatKeys.archived() });
      try {
        getEventBus().emit("chat:restored", { chatId });
      } catch {
        // Event bus may not be initialized — non-fatal
      }
    },
  });
}
