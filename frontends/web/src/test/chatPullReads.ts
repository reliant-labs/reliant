// Copyright (c) 2025 Reliant Labs

/**
 * Transport answers for ChatService's pull reads (the generated keys), for
 * tests that used to mock the chat client by method. Each takes the fake the test
 * already had, called with the arguments the hand wrapper took, and answers
 * the RPC with what that fake returns in the wrapper's shape.
 */

import type {
  GetWorkflowExecutionsRequest,
  ListQueuedAgentMessagesRequest,
  WorkflowExecutionView,
} from "@/gen/reliant/v1/chat_pb";
import type { MockHandlers } from "@/lib/test-utils";

/**
 * An empty answer for every ChatService pull read: for a test that renders a
 * reader of one (the execution tree, a mailbox) but asserts nothing about it.
 * Spread a test's own answers after it to override one.
 */
export function emptyChatPullReads(): MockHandlers {
  return {
    "ChatService.GetWorkflowExecutions": { response: {} },
    "ChatService.ListQueuedAgentMessages": { response: {} },
    "ChatService.ListArchivedChats": { response: {} },
    "ChatService.SearchChats": { response: {} },
    "ChatService.ListStepExecutions": { response: {} },
    "ChatService.GetThreadWorkflowInputs": { response: {} },
    "ChatService.GetWorkflowNodeExecutions": { response: {} },
  };
}

interface TreeAnswer {
  latest?: unknown;
  all?: unknown[];
}

/** `fake(chatId, view)` answers GetWorkflowExecutions as `{ latest, all }`. */
export function workflowExecutionsAnswer(
  fake: (chatId: string, view: WorkflowExecutionView) => TreeAnswer | undefined | Promise<TreeAnswer | undefined>,
): MockHandlers {
  return {
    "ChatService.GetWorkflowExecutions": async (request: unknown) => {
      const { chatId, view } = request as GetWorkflowExecutionsRequest;
      const answer = await fake(chatId, view);
      return { rootWorkflow: answer?.latest ?? undefined, allRootWorkflows: answer?.all ?? [] };
    },
  };
}

interface QueuedRowAnswer {
  id: string;
  body?: string;
  created_at?: string;
  sender_kind?: number;
  attachments?: string[];
}

/** `fake(chatId, threadId)` answers ListQueuedAgentMessages as `{ messages: QueuedAgentMessageView[] }`. */
export function queuedAgentMessagesAnswer(
  fake: (
    chatId: string,
    threadId: string,
  ) => { messages: QueuedRowAnswer[] } | undefined | Promise<{ messages: QueuedRowAnswer[] } | undefined>,
): MockHandlers {
  return {
    "ChatService.ListQueuedAgentMessages": async (request: unknown) => {
      const { chatId, threadId } = request as ListQueuedAgentMessagesRequest;
      const answer = await fake(chatId, threadId);
      return {
        messages: (answer?.messages ?? []).map((row) => ({
          id: row.id,
          body: row.body ?? "",
          createdAt: row.created_at ?? "",
          senderKind: row.sender_kind ?? 0,
          attachments: row.attachments ?? [],
        })),
      };
    },
  };
}
