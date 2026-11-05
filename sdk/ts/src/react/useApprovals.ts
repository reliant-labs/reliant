"use client";
// useApprovals: the one place approvals are read and decided. The card in a
// chat's timeline, the permissions panel and an inbox row all use it.

import { useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { ApprovalService, ApprovalStatus, type Approval, type ListApprovalsByChatResponse } from "#gen/reliant/v1/approval_pb.js";
import type { ToolApprovalUpdate } from "../client/events.js";
import {
  applyApprovalUpdate,
  createApprovalActions,
  type ApprovalActions,
} from "./approvals.js";
import { useReliantClient } from "./context.js";
import { approvalKeys } from "./keys.js";
import { useChatSession } from "./sessions.js";

export interface UseApprovalActionsOptions {
  /**
   * A decision lost to the approval's own end (decided elsewhere, expired, its
   * run over): the approvals leave the pending list and this is told why, in the
   * server's words, instead of the write rolling back. The write still rejects.
   * Without it a lost write rolls back and rejects like any other failure.
   */
  onNotActionable?: (message: string, error: unknown) => void;
}

export interface UseApprovalActionsResult extends ApprovalActions {
  /** A decision is in flight. */
  busy: boolean;
}

/**
 * The four ways to decide approvals, with no read: what a card on a tool call
 * needs, and an inbox row (which holds approvals of many chats). With a chat id
 * a decision flips that chat's cached list at once and puts it back on failure;
 * every decision reads the lists again once the server has recorded it.
 */
export function useApprovalActions(chatId?: string, options: UseApprovalActionsOptions = {}): UseApprovalActionsResult {
  const client = useReliantClient();
  const queryClient = useQueryClient();
  const { onNotActionable } = options;

  const [inFlight, setInFlight] = useState(0);
  const notify = useRef(onNotActionable);
  notify.current = onNotActionable;
  const actions = useMemo(() => {
    const base = createApprovalActions({
      client,
      queryClient,
      chatId,
      onNotActionable: onNotActionable ? (message, error) => notify.current?.(message, error) : undefined,
    });
    const tracked =
      <A extends unknown[]>(fn: (...args: A) => Promise<void>) =>
      async (...args: A): Promise<void> => {
        setInFlight((n) => n + 1);
        try {
          await fn(...args);
        } finally {
          setInFlight((n) => n - 1);
        }
      };
    return {
      approve: tracked(base.approve),
      deny: tracked(base.deny),
      approveMany: tracked(base.approveMany),
      denyMany: tracked(base.denyMany),
    };
    // Whether a handler exists decides how a lost write is treated; the handler itself is read through the ref.
  }, [client, queryClient, chatId, !!onNotActionable]); // eslint-disable-line react-hooks/exhaustive-deps

  return { ...actions, busy: inFlight > 0 };
}

export interface UseApprovalsOptions extends UseApprovalActionsOptions {
  /**
   * Keep the chat's list current from the chat's stream. Default true. A host
   * that already feeds the cache from its own stream of this chat passes false,
   * so the chat is not streamed twice.
   */
  live?: boolean;
  /** Read nothing while false. Default true. */
  enabled?: boolean;
}

export interface UseApprovalsResult extends UseApprovalActionsResult {
  /** Every approval of the chat, oldest first, any status; [] before the first read. */
  approvals: Approval[];
  /** Only the pending ones. */
  pending: Approval[];
  query: UseQueryResult<ListApprovalsByChatResponse>;
}

const EMPTY: Approval[] = [];

/**
 * A chat's approvals and the four ways to decide them. Without a chat id it
 * reads nothing and only decides (see useApprovalActions).
 */
export function useApprovals(chatId?: string, options: UseApprovalsOptions = {}): UseApprovalsResult {
  const client = useReliantClient();
  const queryClient = useQueryClient();
  const { live = true, enabled = true, onNotActionable } = options;

  // The fetch is only the cold-start seed: the stream patches the list live and
  // replays what a reopened chat missed, so a time-based staleTime would add a
  // ListApprovalsByChat on every open, racing the replay that already answers.
  const query = useQuery<ListApprovalsByChatResponse>({
    queryKey: approvalKeys.listByChat({ chatId: chatId ?? "" }),
    queryFn: ({ signal }) => client.service(ApprovalService).listApprovalsByChat({ chatId }, { signal }),
    enabled: !!chatId && enabled,
    staleTime: Infinity,
  });

  const session = useChatSession(chatId, undefined, live && enabled);
  useEffect(() => {
    if (!session || !chatId) return;
    let seen = new Set<ToolApprovalUpdate>();
    const feed = () => {
      const next = session.getState().approvals;
      // The reducer keeps an approval's identity until an event changes it.
      for (const update of next) if (!seen.has(update)) applyApprovalUpdate(queryClient, chatId, update);
      seen = new Set(next);
    };
    feed();
    return session.subscribe(feed);
  }, [session, chatId, queryClient]);

  const actions = useApprovalActions(chatId, { onNotActionable });

  const approvals = query.data?.approvals ?? EMPTY;
  const pending = useMemo(() => {
    const only = approvals.filter((a) => a.status === ApprovalStatus.PENDING);
    return only.length === approvals.length ? approvals : only;
  }, [approvals]);

  return { approvals, pending, query, ...actions };
}
