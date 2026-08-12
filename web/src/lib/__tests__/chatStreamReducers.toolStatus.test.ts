import { describe, it, expect } from "vitest";
import { applyToolCallStateUpdates } from "../chatStreamReducers";
import type { ToolCallState, ToolExecutionStateUpdate } from "../../store/chatStore";

const CHAT_ID = "chat-1";

function state(
  id: string,
  status: ToolCallState["status"],
): Map<string, ToolCallState> {
  return new Map([
    [id, { id, sessionId: CHAT_ID, toolName: "bash", status, timestamp: "t0" }],
  ]);
}

function update(
  id: string,
  status: ToolExecutionStateUpdate["status"],
): ToolExecutionStateUpdate {
  return {
    tool_call_id: id,
    tool_name: "bash",
    status,
    timestamp: "t1",
  } as ToolExecutionStateUpdate;
}

describe("applyToolCallStateUpdates terminal guards", () => {
  // The reported bug: cancelling one tool marked its siblings cancelled too,
  // including ones the user had already seen finish. Whichever terminal status
  // arrives first is the one that describes what the tool actually did.
  it("does not repaint a completed tool as cancelled", () => {
    const next = applyToolCallStateUpdates(
      state("call-a", "completed"),
      [update("call-a", "cancelled")],
      CHAT_ID,
    );

    expect(next.get("call-a")?.status).toBe("completed");
  });

  it("does not repaint a failed tool as cancelled", () => {
    const next = applyToolCallStateUpdates(
      state("call-a", "failed"),
      [update("call-a", "cancelled")],
      CHAT_ID,
    );

    expect(next.get("call-a")?.status).toBe("failed");
  });

  // The existing guard, in the other direction: a completion racing in after
  // the user cancelled must not resurrect the tool.
  it("does not repaint a cancelled tool as completed", () => {
    const next = applyToolCallStateUpdates(
      state("call-a", "cancelled"),
      [update("call-a", "completed")],
      CHAT_ID,
    );

    expect(next.get("call-a")?.status).toBe("cancelled");
  });

  // Cancellation is still the right answer for a tool that never finished.
  it("cancels a tool that was still executing", () => {
    const next = applyToolCallStateUpdates(
      state("call-a", "executing"),
      [update("call-a", "cancelled")],
      CHAT_ID,
    );

    expect(next.get("call-a")?.status).toBe("cancelled");
  });

  // The whole point: one tool's cancellation must not touch its siblings.
  it("cancels only the targeted tool, leaving siblings alone", () => {
    const existing = new Map([
      ...state("done", "completed"),
      ...state("running", "executing"),
    ]);

    const next = applyToolCallStateUpdates(
      existing,
      [update("running", "cancelled")],
      CHAT_ID,
    );

    expect(next.get("running")?.status).toBe("cancelled");
    expect(next.get("done")?.status).toBe("completed");
  });
});
