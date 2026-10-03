import { describe, expect, it, vi } from "vitest";
import {
  WorkflowState,
  WorkflowStopReason,
} from "../../gen/reliant/v1/chat_pb";
import { sendOnExistingChat } from "../chatSendRouting";
import { chatNeedsStart, isWorkflowPending } from "../workflowLifecycle";

function makeActions() {
  return {
    startExistingChat: vi.fn().mockResolvedValue({}),
    sendMessage: vi.fn().mockResolvedValue(undefined),
  };
}

const options = { workflow: null, workflowParams: { a: 1 }, targetThread: "t1" };

describe("workflow lifecycle helpers", () => {
  it("isWorkflowPending is true only for PENDING", () => {
    expect(isWorkflowPending(WorkflowState.PENDING)).toBe(true);
    expect(isWorkflowPending(WorkflowState.ACTIVE)).toBe(false);
    expect(isWorkflowPending(WorkflowState.STOPPED)).toBe(false);
  });

  it("chatNeedsStart handles missing chats and states", () => {
    expect(chatNeedsStart(undefined)).toBe(false);
    expect(chatNeedsStart(null)).toBe(false);
    expect(chatNeedsStart({ workflowState: WorkflowState.PENDING })).toBe(true);
    expect(chatNeedsStart({ workflowState: WorkflowState.ACTIVE })).toBe(false);
  });
});

describe("sendOnExistingChat", () => {
  it("PENDING chat starts with the chat id and never calls sendMessage", async () => {
    const actions = makeActions();
    await sendOnExistingChat(
      { workflowState: WorkflowState.PENDING },
      "chat-1",
      "hi",
      ["att"],
      options,
      actions,
    );
    expect(actions.startExistingChat).toHaveBeenCalledWith("chat-1", "hi", ["att"], {
      workflow: null,
      workflowParams: { a: 1 },
    });
    expect(actions.sendMessage).not.toHaveBeenCalled();
  });

  it("ACTIVE chat goes to sendMessage", async () => {
    const actions = makeActions();
    await sendOnExistingChat(
      { workflowState: WorkflowState.ACTIVE },
      "chat-1",
      "hi",
      undefined,
      options,
      actions,
    );
    expect(actions.sendMessage).toHaveBeenCalledWith("chat-1", "hi", undefined, options);
    expect(actions.startExistingChat).not.toHaveBeenCalled();
  });

  it("paused chat goes to sendMessage", async () => {
    const actions = makeActions();
    const paused = {
      workflowState: WorkflowState.STOPPED,
      workflowStopReason: WorkflowStopReason.PAUSED,
    };
    await sendOnExistingChat(paused, "chat-1", "hi", undefined, options, actions);
    expect(actions.sendMessage).toHaveBeenCalledTimes(1);
    expect(actions.startExistingChat).not.toHaveBeenCalled();
  });
});
