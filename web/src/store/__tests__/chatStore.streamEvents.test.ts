import { beforeEach, describe, expect, it } from "vitest";
import type { ChatUpdate } from "../../types/streaming";
import { useChatStore } from "../chatStore";
import { useThreadActivityStore } from "../threadActivityStore";

// Characterization tests for the workflow event-log reducers inside
// processChatStreamUpdates: errorEvents, infoEvents, runOutputs, and
// nodeExecutions. These are stream-only ephemeral logs (no fetch API) that the
// store appends live and replaces on snapshot. Locking the exact append /
// dedup / snapshot / sort behavior here lets us extract each into a pure
// reducer without changing what the UI sees.

function seedChat(chatId: string) {
  useChatStore.setState({
    activeChatId: null,
    messages: {},
    toolResultsByCallId: {},
    streamingMessages: {},
    errorEvents: {},
    infoEvents: {},
    runOutputs: {},
    nodeExecutions: {},
    toolCallStates: {},
  } as never);
}

function errorUpdate(id: string, message: string, ts: string): ChatUpdate {
  return {
    update_type: "error",
    id,
    chat_id: "c1",
    activity_id: "act",
    error_message: message,
    timestamp: ts,
    sequence_number: 0,
  } as unknown as ChatUpdate;
}

function infoUpdate(id: string, message: string, ts: string): ChatUpdate {
  return {
    update_type: "info",
    id,
    chat_id: "c1",
    message,
    timestamp: ts,
  } as unknown as ChatUpdate;
}

function runOutput(
  uniqueActivityId: string,
  seq: number,
  content: string,
): ChatUpdate {
  return {
    update_type: "run_output",
    id: `ro-${uniqueActivityId}`,
    step_id: "s1",
    unique_activity_id: uniqueActivityId,
    sequence_number: seq,
    timestamp: "2026-01-01T00:00:00.000Z",
    content,
  } as unknown as ChatUpdate;
}

function nodeExec(
  nodeId: string,
  eventType: string,
  seq: number,
): ChatUpdate {
  return {
    update_type: "node_execution",
    node_id: nodeId,
    event_type: eventType,
    status: 0,
    sequence_number: seq,
  } as unknown as ChatUpdate;
}

function toolCall(
  contentBlockId: string,
  status: string,
  toolName = "bash",
): ChatUpdate {
  return {
    update_type: "tool_call",
    content_block_id: contentBlockId,
    tool_name: toolName,
    status,
    node_id: "",
    sequence_number: 0,
  } as unknown as ChatUpdate;
}

function threadUpdate(
  id: string,
  fields: Record<string, unknown> = {},
): ChatUpdate {
  return {
    update_type: "thread",
    id,
    chat_id: "c1",
    ...fields,
  } as unknown as ChatUpdate;
}

const CHAT = "c1";
beforeEach(() => {
  seedChat(CHAT);
  useThreadActivityStore.setState({ threads: {} } as never);
});

describe("errorEvents reducer", () => {
  it("appends distinct errors and dedups by id (replace in place)", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      errorUpdate("e1", "first", "2026-01-01T00:00:01.000Z"),
      errorUpdate("e2", "second", "2026-01-01T00:00:02.000Z"),
    ]);
    store.processChatStreamUpdates(CHAT, [
      errorUpdate("e1", "first-updated", "2026-01-01T00:00:03.000Z"),
    ]);

    const events = useChatStore.getState().errorEvents[CHAT] || [];
    expect(events.map((e) => e.id)).toEqual(["e1", "e2"]);
    expect(events.find((e) => e.id === "e1")?.error_message).toBe(
      "first-updated",
    );
  });

  it("replaces the whole list on snapshot", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      errorUpdate("e1", "live", "2026-01-01T00:00:01.000Z"),
    ]);
    store.processChatStreamUpdates(
      CHAT,
      [errorUpdate("e2", "snapshot", "2026-01-01T00:00:02.000Z")],
      true,
    );

    const events = useChatStore.getState().errorEvents[CHAT] || [];
    expect(events.map((e) => e.id)).toEqual(["e2"]);
  });
});

describe("infoEvents reducer", () => {
  it("dedups by id and preserves the original timestamp on update", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      infoUpdate("i1", "hello", "2026-01-01T00:00:01.000Z"),
    ]);
    store.processChatStreamUpdates(CHAT, [
      infoUpdate("i1", "hello-edited", "2026-01-01T00:00:09.000Z"),
    ]);

    const events = useChatStore.getState().infoEvents[CHAT] || [];
    expect(events).toHaveLength(1);
    expect(events[0].message).toBe("hello-edited");
    // Original timestamp preserved for stable timeline placement.
    expect(events[0].timestamp).toBe("2026-01-01T00:00:01.000Z");
  });
});

describe("runOutputs reducer", () => {
  it("dedups by unique_activity_id and sorts by sequence_number", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      runOutput("a", 2, "second"),
      runOutput("b", 1, "first"),
    ]);
    store.processChatStreamUpdates(CHAT, [runOutput("a", 2, "second-updated")]);

    const outputs = useChatStore.getState().runOutputs[CHAT] || [];
    expect(outputs.map((r) => r.unique_activity_id)).toEqual(["b", "a"]);
    expect(outputs.find((r) => r.unique_activity_id === "a")?.content).toBe(
      "second-updated",
    );
  });

  it("keeps run outputs with no unique_activity_id distinct (never dedups a falsy key)", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      runOutput("", 1, "first"),
      runOutput("", 2, "second"),
    ]);

    const outputs = useChatStore.getState().runOutputs[CHAT] || [];
    expect(outputs.map((r) => r.content)).toEqual(["first", "second"]);
  });
});

describe("nodeExecutions reducer", () => {
  it("dedups by node_id + event_type and sorts by sequence_number", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      nodeExec("n1", "started", 1),
      nodeExec("n1", "completed", 3),
      nodeExec("n2", "started", 2),
    ]);
    // Same node_id + event_type replaces; different event_type is distinct.
    store.processChatStreamUpdates(CHAT, [nodeExec("n1", "started", 1)]);

    const execs = useChatStore.getState().nodeExecutions[CHAT] || [];
    expect(execs.map((n) => `${n.node_id}:${n.event_type}`)).toEqual([
      "n1:started",
      "n2:started",
      "n1:completed",
    ]);
  });
});

describe("thread updates", () => {
  it("appends new threads and merges by id, preserving identity fields on completion updates", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [
      threadUpdate("th1", {
        thread_title: "Research",
        spawned_by_node_id: "node-9",
        router_decision: "route-a",
      }),
    ]);
    // A later completion update that omits identity fields must not wipe them.
    store.processChatStreamUpdates(CHAT, [
      threadUpdate("th1", { status: "completed" }),
    ]);

    const threads = useThreadActivityStore.getState().threads[CHAT] || [];
    expect(threads).toHaveLength(1);
    expect(threads[0].thread_title).toBe("Research");
    expect(threads[0].spawned_by_node_id).toBe("node-9");
    expect(threads[0].router_decision).toBe("route-a");
  });
});

describe("toolCallStates reducer", () => {
  it("maps denied -> failed and tracks state by tool_call_id", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [toolCall("t1", "denied")]);

    const states = useChatStore.getState().toolCallStates[CHAT];
    expect(states?.get("t1")?.status).toBe("failed");
  });

  it("does not let a late 'completed' overwrite a terminal cancelled/backgrounded state", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [toolCall("t1", "cancelled")]);
    // A completion racing in after cancel must not resurrect the tool.
    store.processChatStreamUpdates(CHAT, [toolCall("t1", "completed")]);

    expect(useChatStore.getState().toolCallStates[CHAT]?.get("t1")?.status).toBe(
      "cancelled",
    );
  });

  it("allows normal status progression before a terminal state", () => {
    const store = useChatStore.getState();
    store.processChatStreamUpdates(CHAT, [toolCall("t1", "pending")]);
    store.processChatStreamUpdates(CHAT, [toolCall("t1", "executing")]);
    store.processChatStreamUpdates(CHAT, [toolCall("t1", "completed")]);

    expect(useChatStore.getState().toolCallStates[CHAT]?.get("t1")?.status).toBe(
      "completed",
    );
  });
});