// Copyright (c) 2025 Reliant Labs

/**
 * Everything run detail knows about how a run was launched, in one place:
 * the launch event, the parent chat an agent started it from, and the
 * automation's timezone. Kept out of RunDetailPage so the page stays layout.
 *
 * The parent's title comes from GetChat on the event's parent_chat_id. That
 * call is owner-scoped, so a parent that was deleted or is not the caller's
 * fails, and the run reads "Started by an agent" rather than naming a chat
 * the user cannot open.
 */

import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import type { LaunchEvent } from "@/api/run-grpc";
import { triggerSchedule } from "@/api/trigger-grpc";
import { chatKeys } from "@/hooks/chat-queries";
import { useLaunchEvent } from "@/hooks/run-queries";
import { useTriggerById } from "@/hooks/trigger-queries";

export interface RunLaunch {
  /** Still loading; render no launch-derived text yet (§4.4). */
  loading: boolean;
  /** Null for a run that predates launch events; undefined while loading or on error. */
  event: LaunchEvent | null | undefined;
  /** The chat an agent started this run from, when it is the caller's and still exists. */
  parent?: { chatId: string; title: string };
  /** The automation's name now; undefined when deleted or not a scheduled run. */
  triggerName?: string;
  /** The automation's schedule timezone. */
  timezone?: string;
}

export function useRunLaunch(chat: { id: string; launchKind?: string; triggerId?: string }): RunLaunch {
  const launch = useLaunchEvent(chat.id);
  const event = launch.data;
  const parentChatId = event?.parentChatId;
  // Not useChat: its retry and refetch-on-focus are for a chat being viewed.
  // A parent that is gone stays gone, so one failed lookup is the answer.
  const parent = useQuery({
    queryKey: [...chatKeys.detail(parentChatId), "as-parent"] as const,
    queryFn: () => api.chatsV2.get(parentChatId!),
    enabled: !!parentChatId,
    retry: false,
    staleTime: 60_000,
  });
  const trigger = useTriggerById(chat.triggerId);

  return {
    loading: launch.isLoading || (!!parentChatId && parent.isLoading),
    event: launch.isError ? undefined : event,
    parent: parentChatId && parent.data ? { chatId: parentChatId, title: parent.data.title } : undefined,
    triggerName: trigger?.name,
    timezone: trigger ? triggerSchedule(trigger)?.timezone : undefined,
  };
}
