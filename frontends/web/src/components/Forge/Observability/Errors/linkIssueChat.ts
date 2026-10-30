// Copyright (c) 2025 Reliant Labs

/**
 * Record that a chat was started from an issue ("Ask Reliant").
 *
 * AskReliantButton's onChatCreated fires on the reader's first send, from the
 * new-chat screen, after the console has unmounted. So this takes everything it
 * needs as plain values captured at render (the api object, the query client,
 * the issue id) and calls no hook. LinkIssueChat requires only the issue id and
 * the chat id; the title is optional and is not known yet (the chat has no
 * title until its first turn), so none is sent and the timeline names the chat
 * by its id until one is recorded.
 *
 * Best effort by design: the chat exists whether or not the link was written,
 * so a failure is logged and never thrown into the composer.
 */

import type { QueryClient } from "@tanstack/react-query";

import { issueKeys } from "@/hooks/observability-queries";
import { logger } from "@/lib/logger";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

export function linkChatToIssue(api: Pick<ObservabilityApi, "linkIssueChat">, queryClient: QueryClient, issueId: string): (chatId: string) => void {
  return (chatId) => {
    void api
      .linkIssueChat({ issueId, chatId })
      .catch((error: unknown) => logger.warn("[IssueDetail] could not record the chat on the issue", { issueId, chatId, error }))
      .finally(() => void queryClient.invalidateQueries({ queryKey: issueKeys.timelines() }));
  };
}
