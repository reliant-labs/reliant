// @vitest-environment jsdom
import { act, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { harness, settle } from "../test/react-harness.js";
import { CHAT, caughtUpFrame, questionData, snapshotFrame, textMessage, updatesFrame } from "../test/scripted-server.js";
import { useQuestions } from "./useQuestions.js";

const q = (id: string, extra: Record<string, unknown> = {}) => ({
  questionId: id,
  chatId: CHAT,
  workflowId: "wf-1",
  stepId: "s",
  status: "pending",
  threadId: CHAT,
  ...extra,
});

describe("useQuestions", () => {
  it("reads the pending and the async lists once and shares them", async () => {
    let pendingReads = 0;
    const h = harness({
      ListPendingQuestions: () => (pendingReads++, { questions: [q("q1")] }),
      ListAsyncQuestions: () => ({ questions: [q("q2", { async: true, toolCallId: "tc-2" })] }),
    });
    const a = h.render(() => useQuestions(CHAT, { live: false }));
    const b = h.render(() => useQuestions(CHAT, { live: false }));
    await waitFor(() => expect(a.result.current.pending).toHaveLength(1));
    await waitFor(() => expect(b.result.current.async).toHaveLength(1));
    expect(a.result.current.pending[0]!.questionId).toBe("q1");
    expect(b.result.current.async[0]).toMatchObject({ questionId: "q2", toolCallId: "tc-2" });
    expect(pendingReads).toBe(1);
  });

  it("answering sends the answers as a reply, drops the question at once, and reads the lists again", async () => {
    const resolved: Array<Record<string, unknown>> = [];
    let pendingReads = 0;
    const h = harness({
      ListPendingQuestions: () => (pendingReads++, { questions: pendingReads > 1 ? [] : [q("q1"), q("q3")] }),
      ListAsyncQuestions: () => ({ questions: [] }),
      ResolveQuestion: (r) => (resolved.push(r), { success: true }),
    });
    const { result } = h.render(() => useQuestions(CHAT, { live: false }));
    await waitFor(() => expect(result.current.pending).toHaveLength(2));

    const answers = { answers: [{ question: "Which env?", selected: ["staging"] }] };
    await act(() => result.current.answer("q1", answers));
    expect(resolved).toEqual([expect.objectContaining({ questionId: "q1", action: "reply", responseData: JSON.stringify(answers) })]);
    // Only that question left; the read after the write then replaces the list with the server's.
    await waitFor(() => expect(pendingReads).toBe(2));
  });

  it("keeps the lists current from the chat stream: pending adds, resolved removes only its own, async stays listed answered", async () => {
    const h = harness({
      ListPendingQuestions: () => ({ questions: [] }),
      ListAsyncQuestions: () => ({ questions: [] }),
    });
    const { result } = h.render(() => useQuestions(CHAT));
    await settle();

    const conn = h.server.chatStreams()[0]!;
    act(() => {
      conn.push(snapshotFrame(5, [textMessage("m-1", 1, 1, "hi")]));
      conn.push(caughtUpFrame(5));
      conn.push(
        updatesFrame(
          [
            questionData(6, "q1", "pending", { metadata: '{"questions":[]}' }),
            questionData(7, "q2", "pending", { thread_id: "t-2" }),
            questionData(8, "qa", "pending", { async: true, tool_call_id: "tc-9" }),
          ],
          8,
        ),
      );
    });
    await waitFor(() => expect(result.current.pending.map((x) => x.questionId)).toEqual(["q1", "q2"]));
    expect(result.current.pending[1]).toMatchObject({ threadId: "t-2" });
    expect(result.current.async.map((x) => x.questionId)).toEqual(["qa"]);

    act(() => {
      conn.push(updatesFrame([questionData(9, "q1", "resolved"), questionData(10, "qa", "resolved", { async: true, withdrawn_reason: "obsolete" })], 10));
    });
    await waitFor(() => expect(result.current.pending.map((x) => x.questionId)).toEqual(["q2"]));
    expect(result.current.async[0]).toMatchObject({ questionId: "qa", status: "resolved", withdrawnReason: "obsolete" });
  });

  it("an async question a reconnect snapshot no longer carries is resolved in the cache, not left pending", async () => {
    const h = harness({
      ListPendingQuestions: () => ({ questions: [] }),
      ListAsyncQuestions: () => ({ questions: [] }),
    });
    const { result } = h.render(() => useQuestions(CHAT));
    await settle();

    const conn = h.server.chatStreams()[0]!;
    act(() => {
      conn.push(snapshotFrame(5, [textMessage("m-1", 1, 1, "hi")], [questionData(5, "qa", "pending", { async: true, tool_call_id: "tc-9" })]));
      conn.push(caughtUpFrame(5));
    });
    await waitFor(() => expect(result.current.async.map((x) => [x.questionId, x.status])).toEqual([["qa", "pending"]]));

    // It was answered while the chat was away: the server's snapshot has no trace of it.
    act(() => conn.push(snapshotFrame(9, [textMessage("m-2", 1, 2, "later")])));
    await waitFor(() => expect(result.current.async.map((x) => [x.questionId, x.status])).toEqual([["qa", "resolved"]]));
  });

  it("reads nothing without a chat", async () => {
    const h = harness();
    const { result } = h.render(() => useQuestions(undefined));
    await settle();
    expect(result.current.pending).toEqual([]);
    expect(h.server.unaryRequests).toHaveLength(0);
    await expect(result.current.answer("q", { answers: [] })).rejects.toThrow(/no chat id/);
  });
});
