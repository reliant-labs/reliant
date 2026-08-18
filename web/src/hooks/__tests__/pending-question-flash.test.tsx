import { describe, expect, it, beforeEach, afterEach, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import React from "react";

// The hook fetches through question-grpc; stub it so this characterizes
// cache/render behavior rather than transport.
vi.mock("../../api/question-grpc", () => ({
  questionGrpc: {
    getPendingQuestion: vi.fn(async () => null),
    resolveQuestion: vi.fn(async () => ({})),
  },
}));

import { queryClient } from "../../lib/query-client";
import { patchPendingQuestionCache, usePendingQuestion } from "../approval-queries";

// Evidence for WHERE the "already-answered ask briefly pops up when I open a
// chat" flash had to be fixed.
//
// The chat stream drives this cache: a "pending" question update sets it,
// "resolved" clears it. The test below pins the consequence — a stale "pending"
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

const question = {
  question_id: "q1",
  chat_id: "c1",
  workflow_id: "w1",
  step_id: "s1",
  status: "pending",
  created_at: "",
  metadata: '{"type":"ask_user"}',
} as never;

beforeEach(() => queryClient.clear());
afterEach(() => queryClient.clear());

describe("pending question visibility on chat open", () => {
  it("paints a stale pending question that reaches the cache", async () => {
    const seen: unknown[] = [];
    const { result } = renderHook(
      () => {
        const q = usePendingQuestion("c1");
        seen.push(q.data);
        return q;
      },
      { wrapper },
    );
    await waitFor(() => expect(result.current.isFetching).toBe(false));
    seen.length = 0; // drop initial-load renders

    // A superseded "pending" arriving on its own — the snapshot replaying an
    // answered question, or a resolve path that never announced itself.
    patchPendingQuestionCache("c1", question);
    await waitFor(() => expect(result.current.data).not.toBeNull());

    expect(
      seen.some((v) => (v as { question_id?: string } | null)?.question_id === "q1"),
      "a pending question in the cache is rendered with no guard in between, " +
        "which is why the flash must be prevented upstream rather than hidden here",
    ).toBe(true);
  });
});
