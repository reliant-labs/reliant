import { describe, it, expect } from "vitest";
import {
  applyToolCallStateUpdates,
  toolStatusSurvivesStreamAbort,
} from "../chatStreamReducers";
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

  // "backgrounded" is a promise of a later outcome, not an outcome. When the
  // backgrounded process exits, the server closes the call and streams its
  // real status — and that update has to land. Treating "backgrounded" as
  // terminal threw the completion away, so an open chat kept showing a running
  // process for a command that had finished, until the next reload.
  it("lets a backgrounded tool's real outcome replace it", () => {
    const completed = applyToolCallStateUpdates(
      state("bg", "backgrounded"),
      [update("bg", "completed")],
      CHAT_ID,
    );
    expect(completed.get("bg")?.status).toBe("completed");

    const failed = applyToolCallStateUpdates(
      state("bg", "backgrounded"),
      [update("bg", "failed")],
      CHAT_ID,
    );
    expect(failed.get("bg")?.status).toBe("failed");
  });

  // ...but ONLY a reported outcome. A stale "executing" or the abort pass's
  // inferred cancel says nothing about a process that outlives the stream.
  it("keeps a backgrounded tool backgrounded against stale or inferred updates", () => {
    const stale = applyToolCallStateUpdates(
      state("bg", "backgrounded"),
      [update("bg", "executing")],
      CHAT_ID,
    );
    expect(stale.get("bg")?.status).toBe("backgrounded");

    const inferred = applyToolCallStateUpdates(
      state("bg", "backgrounded"),
      [{ ...update("bg", "cancelled"), inferred: true }],
      CHAT_ID,
    );
    expect(inferred.get("bg")?.status).toBe("backgrounded");

    // A reported cancel (the server closing a killed process) does land.
    const reported = applyToolCallStateUpdates(
      state("bg", "backgrounded"),
      [update("bg", "cancelled")],
      CHAT_ID,
    );
    expect(reported.get("bg")?.status).toBe("cancelled");
  });

  // Backgrounded still survives a stream ending underneath it: the user asked
  // the process to outlive the turn, so the turn stopping says nothing about it.
  it("still treats backgrounded as surviving a stream abort", () => {
    expect(toolStatusSurvivesStreamAbort("backgrounded")).toBe(true);
    expect(toolStatusSurvivesStreamAbort("executing")).toBe(false);
  });
});
