import { describe, expect, it, beforeEach, afterEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import React from "react";

import { create } from "@bufbuild/protobuf";
import { QuestionInfoSchema } from "../../gen/reliant/v1/question_pb";
import { setTransport } from "../../lib/connect";
import { mockTransport } from "../../lib/test-utils";
import { queryClient } from "../../lib/query-client";
import { upsertPendingQuestionInCache, usePendingQuestions } from "../question-queries";

// The hook fetches through the transport; answer it in memory so this
// characterizes cache/render behavior rather than transport.
setTransport(mockTransport({ "QuestionService.ListPendingQuestions": { response: { questions: [] } } }));

// Evidence for WHERE the "already-answered ask briefly pops up when I open a
// chat" flash had to be fixed.
//
// The chat stream drives this cache: a "pending" question update adds it,
// "resolved" removes it. The test below pins the consequence — a stale "pending"
// that reaches the cache at all IS painted. There is no render-layer guard
// between the cache and the screen.
//
// So the flash cannot be fixed here without masking it (a delay or a spinner).
// It has to be prevented upstream, by never handing the client a superseded
// "pending" in the first place:
//   - the chat snapshot now dedups question updates to their latest status, so
//     an answered question's opening row is no longer replayed on open
//     (GetLatestNonMessageUpdatesPerEntity), and
//   - the cancel and timeout resolve paths now emit "resolved", so the feed's
//     newest question row can no longer be a "pending" that nothing supersedes.
//
// This file deliberately does NOT assert that a pending+resolved pair applied
// in ONE synchronous batch stays invisible. That coalescing is a React Query
// invariant rather than behavior this codebase controls, and a test for it
// passes no matter what the app does — verified by probe.

const wrapper = ({ children }: { children: ReactNode }) =>
  React.createElement(QueryClientProvider, { client: queryClient }, children);

const question = create(QuestionInfoSchema, {
  questionId: "q1",
  chatId: "c1",
  workflowId: "w1",
  threadId: "c1",
  stepId: "s1",
  status: "pending",
  metadata: '{"type":"ask_user"}',
});

beforeEach(() => queryClient.clear());
afterEach(() => queryClient.clear());

describe("pending question visibility on chat open", () => {
  it("paints a stale pending question that reaches the cache", async () => {
    const seen: unknown[] = [];
    const { result } = renderHook(
      () => {
        const q = usePendingQuestions("c1");
        seen.push(q.data);
        return q;
      },
      { wrapper },
    );
    await waitFor(() => expect(result.current.isFetching).toBe(false));
    seen.length = 0; // drop initial-load renders

    // A superseded "pending" arriving on its own — the snapshot replaying an
    // answered question, or a resolve path that never announced itself.
    upsertPendingQuestionInCache("c1", question);
    await waitFor(() => expect(result.current.data?.questions).toHaveLength(1));

    expect(
      seen.some((v) =>
        (v as { questions: { questionId: string }[] } | undefined)?.questions.some((q) => q.questionId === "q1"),
      ),
      "a pending question in the cache is rendered with no guard in between, " +
        "which is why the flash must be prevented upstream rather than hidden here",
    ).toBe(true);
  });
});
