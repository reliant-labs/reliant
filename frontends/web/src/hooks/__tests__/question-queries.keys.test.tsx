/**
 * QuestionService reads and writes share the generated key scope, so a
 * mounted question list is never left stale by a write or a stream event:
 *
 *  - answering (resolvePendingQuestion, the path every surface uses, and the
 *    generated useResolveQuestion) invalidates `questionServiceKeys.all`, and
 *    the mounted list refetches what the server now says;
 *  - a stream event patches the very entry the generated hook reads.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import {
  applyQuestionUpdate,
  resolvePendingQuestion,
  useAsyncQuestions,
  usePendingQuestions,
} from "../question-queries";
import { questionServiceKeys, useResolveQuestion } from "../question-service-hooks_gen";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

type Row = { questionId: string; threadId: string; status: string; async: boolean; toolCallId: string };
let server: Row[];
let calls: Record<string, number>;

beforeEach(() => {
  server = [
    { questionId: "qA", threadId: "c1", status: "pending", async: false, toolCallId: "" },
    { questionId: "qB", threadId: "spawn-b", status: "pending", async: false, toolCallId: "" },
  ];
  calls = { pending: 0, async: 0 };
  setTransport(
    mockTransport({
      "QuestionService.ListPendingQuestions": () => {
        calls.pending++;
        return { questions: server.filter((q) => !q.async && q.status === "pending") };
      },
      "QuestionService.ListAsyncQuestions": () => {
        calls.async++;
        return { questions: server.filter((q) => q.async) };
      },
      "QuestionService.ResolveQuestion": (req) => {
        const { questionId } = req as { questionId: string };
        server = server.map((q) => (q.questionId === questionId ? { ...q, status: "resolved" } : q));
        return {};
      },
    }),
  );
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

const ids = (data: { questions: { questionId: string }[] } | undefined) => data?.questions.map((q) => q.questionId);

describe("QuestionService keys", () => {
  it("resolvePendingQuestion refetches the mounted pending list", async () => {
    const list = renderHook(() => usePendingQuestions("c1"), { wrapper });
    await waitFor(() => expect(ids(list.result.current.data)).toEqual(["qA", "qB"]));

    // The server resolves BOTH (say another tab answered qB meanwhile); the
    // local patch only knows about qA, the refetch brings the rest.
    server = server.map((q) => (q.questionId === "qB" ? { ...q, status: "resolved" } : q));
    await resolvePendingQuestion("c1", "qA", "reply", "{}");

    await waitFor(() => expect(ids(list.result.current.data)).toEqual([]));
    expect(calls.pending).toBe(2);
  });

  it("the generated useResolveQuestion invalidates the same entries", async () => {
    const list = renderHook(() => usePendingQuestions("c1"), { wrapper });
    await waitFor(() => expect(ids(list.result.current.data)).toEqual(["qA", "qB"]));

    const resolve = renderHook(() => useResolveQuestion(), { wrapper });
    await resolve.result.current.mutateAsync({ questionId: "qB", action: "reply" });

    await waitFor(() => expect(ids(list.result.current.data)).toEqual(["qA"]));
  });

  it("questionServiceKeys.all reaches both lists", async () => {
    renderHook(() => usePendingQuestions("c1"), { wrapper });
    renderHook(() => useAsyncQuestions("c1"), { wrapper });
    await waitFor(() => expect(calls).toEqual({ pending: 1, async: 1 }));

    await queryClient.invalidateQueries({ queryKey: questionServiceKeys.all });
    await waitFor(() => expect(calls).toEqual({ pending: 2, async: 2 }));
  });

  it("a stream event updates the entry the generated hook reads", async () => {
    const list = renderHook(() => usePendingQuestions("c1"), { wrapper });
    await waitFor(() => expect(ids(list.result.current.data)).toEqual(["qA", "qB"]));

    applyQuestionUpdate("c1", {
      update_type: "question",
      question_id: "qA",
      chat_id: "c1",
      workflow_id: "c1",
      step_id: "s",
      status: "resolved",
    });

    await waitFor(() => expect(ids(list.result.current.data)).toEqual(["qB"]));
    // Patched, not refetched: the stream carried the answer.
    expect(calls.pending).toBe(1);
  });
});
