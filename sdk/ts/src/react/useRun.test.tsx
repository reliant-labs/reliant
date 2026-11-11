// @vitest-environment jsdom
import { act, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { harness, settle } from "../test/react-harness.js";
import { CHAT, caughtUpFrame, messageData, snapshotFrame, textMessage, updatesFrame } from "../test/scripted-server.js";
import { useRun } from "./useRun.js";

const RUN = "5ca1ab1e-0000-0000-0000-000000000001";

describe("useRun", () => {
  it("reads the run and streams the chat it writes to", async () => {
    const h = harness({
      GetRun: (r) => ({
        run: { id: r.runId, workflowName: "builtin://agent", sessionId: CHAT },
        nodes: [{ id: "n1", nodeId: "build", status: "completed" }],
        nodesTruncated: false,
      }),
    });
    const { result } = h.render(() => useRun(RUN));
    await waitFor(() => expect(result.current.run?.id).toBe(RUN));
    expect(result.current.nodes.map((n) => n.nodeId)).toEqual(["build"]);

    // The chat is the run's session, not the run id.
    await waitFor(() => expect(h.server.chatStreams().some((c) => c.request.chatId === CHAT)).toBe(true));
    const conn = h.server.chatStreams().find((c) => c.request.chatId === CHAT)!;
    act(() => {
      conn.push(snapshotFrame(3, [textMessage("m-1", 1, 1, "start")]));
      conn.push(caughtUpFrame(3));
      conn.push(updatesFrame([messageData(4, "m-2", "built")], 4));
    });
    await settle();
    expect(result.current.chat.messages.map((m) => m.contentBlocks[0]?.content)).toEqual(["start", "built"]);
    expect(result.current.chat.status).toBe("connected");
  });

  it("uses the run id as the chat id for a run with no session", async () => {
    const h = harness({ GetRun: (r) => ({ run: { id: r.runId, sessionId: "" } }) });
    const { result } = h.render(() => useRun(RUN));
    await waitFor(() => expect(result.current.run?.id).toBe(RUN));
    await settle();
    expect(h.server.chatStreams().at(-1)!.request.chatId).toBe(RUN);
  });

  it("cancels the run and reads it again", async () => {
    let reads = 0;
    const cancelled: string[] = [];
    const h = harness({
      GetRun: (r) => (reads++, { run: { id: r.runId, sessionId: CHAT } }),
      CancelRun: (r) => (cancelled.push(r.runId as string), { success: true }),
    });
    const { result } = h.render(() => useRun(RUN));
    await waitFor(() => expect(result.current.run).not.toBeNull());
    await act(() => result.current.cancel());
    expect(cancelled).toEqual([RUN]);
    await waitFor(() => expect(reads).toBe(2));
  });

  it("reads nothing without a run id", async () => {
    const h = harness();
    const { result } = h.render(() => useRun(undefined));
    await settle();
    expect(result.current.run).toBeNull();
    expect(h.server.unaryRequests).toHaveLength(0);
    expect(h.server.connections).toHaveLength(0);
    await expect(result.current.cancel()).rejects.toThrow(/no run id/);
  });
});
