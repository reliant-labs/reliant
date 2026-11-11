"use client";
// useRun: a run, and the chat it writes to. A run's id is its workflow id; the
// chat is the session it belongs to (a headless run has none: the run id is
// then the chat id, the same fallback the Reliant app uses).

import { useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { useCallback } from "react";
import { RunService, type GetRunResponse, type Run } from "#gen/reliant/v1/run_pb.js";
import { useReliantClient } from "./context.js";
import { runKeys } from "./keys.js";
import { useChat, type UseChatOptions, type UseChatResult } from "./useChat.js";

export interface UseRunResult {
  run: Run | null;
  /** Persisted node attempts, newest first, including nested workflows. */
  nodes: GetRunResponse["nodes"];
  nodesTruncated: boolean;
  /** The run's chat, live: its transcript, gates and workflow/node execution state. */
  chat: UseChatResult;
  query: UseQueryResult<GetRunResponse>;
  /** Stop the run for good. */
  cancel(): Promise<void>;
}

export function useRun(runId: string | undefined, options: UseChatOptions = {}): UseRunResult {
  const client = useReliantClient();
  const queryClient = useQueryClient();
  const enabled = (options.enabled ?? true) && !!runId;

  const query = useQuery<GetRunResponse>({
    queryKey: runKeys.get({ runId: runId ?? "" }),
    queryFn: ({ signal }) => client.service(RunService).getRun({ runId }, { signal }),
    enabled,
  });

  const run = query.data?.run ?? null;
  // Until the run is read the run id is the best guess: a run started from a chat shares its id.
  const chatId = enabled ? run?.sessionId || runId : undefined;
  const chat = useChat(chatId, options);

  const cancel = useCallback(async () => {
    if (!runId) throw new Error("@reliantlabs/sdk/react: no run id");
    await client.service(RunService).cancelRun({ runId });
    void queryClient.invalidateQueries({ queryKey: runKeys.all });
  }, [client, queryClient, runId]);

  return {
    run,
    nodes: query.data?.nodes ?? [],
    nodesTruncated: query.data?.nodesTruncated ?? false,
    chat,
    query,
    cancel,
  };
}
