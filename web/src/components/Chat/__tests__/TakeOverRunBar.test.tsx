/**
 * The composer on an automation run nobody has taken over (WORKFLOW_UI.md
 * §4.2, §6.3) is collapsed to a bar that says replying adopts it. Expanding
 * commits nothing; a successful send adopts; a failed send does not.
 */
import { act, fireEvent, renderHook, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithQuery } from "../../../test/renderWithQuery";

const { adopt } = vi.hoisted(() => ({ adopt: vi.fn() }));

vi.mock("../../../hooks/run-queries", () => ({
  useAdoptRun: () => ({ mutateAsync: adopt }),
}));
vi.mock("../../../hooks/trigger-queries", () => ({
  useTriggerName: (id?: string) => (id === "trg-1" ? "Nightly triage" : undefined),
}));

import { TakeOverRunBar, useAdoptOnSend } from "../TakeOverRunBar";

const scheduled = { id: "chat-1", launchKind: "schedule", triggerId: "trg-1" };

// Reset only. Chaining mockResolvedValue here makes vitest 4 report a later
// throwing mockImplementation as a test failure even when the code under test
// catches it, which would hide exactly the "adoption failed" case below.
beforeEach(() => {
  adopt.mockReset();
  adopt.mockImplementation(async () => ({}));
});

describe("TakeOverRunBar", () => {
  it("names the automation and expands on Reply without adopting", () => {
    const onReply = vi.fn();
    renderWithQuery(<TakeOverRunBar chat={scheduled} onReply={onReply} />);

    expect(screen.getByTestId("take-over-run-bar")).toHaveTextContent("Started by Nightly triage.");
    fireEvent.click(screen.getByRole("button", { name: "Reply to take it over" }));
    expect(onReply).toHaveBeenCalledOnce();
    expect(adopt).not.toHaveBeenCalled();
  });
});

describe("useAdoptOnSend", () => {
  it("adopts an un-adopted automation chat after the send succeeds", async () => {
    const order: string[] = [];
    const send = vi.fn(async () => {
      order.push("send");
    });
    adopt.mockImplementation(async () => {
      order.push("adopt");
    });
    const { result } = renderHook(() => useAdoptOnSend(scheduled, send));

    await act(() => result.current("hello"));

    expect(send).toHaveBeenCalledWith("hello");
    expect(adopt).toHaveBeenCalledWith("chat-1");
    expect(order).toEqual(["send", "adopt"]);
  });

  it("does not adopt when the send fails", async () => {
    const send = vi.fn(async () => {
      throw new Error("daemon offline");
    });
    const { result } = renderHook(() => useAdoptOnSend(scheduled, send));

    await expect(act(() => result.current("hello"))).rejects.toThrow("daemon offline");
    expect(adopt).not.toHaveBeenCalled();
  });

  it("leaves interactive and already-adopted chats alone", async () => {
    const send = vi.fn(async () => {});
    const interactive = renderHook(() => useAdoptOnSend({ id: "c", launchKind: "chat.start" }, send));
    const adopted = renderHook(() =>
      useAdoptOnSend({ ...scheduled, adoptedAt: "2024-01-01T00:00:00Z" }, send),
    );

    await act(() => interactive.result.current("a"));
    await act(() => adopted.result.current("b"));
    expect(adopt).not.toHaveBeenCalled();
  });

  it("keeps the reply when adoption fails", async () => {
    adopt.mockImplementation(async () => {
      throw new Error("adopt failed");
    });
    const send = vi.fn(async () => {});
    const { result } = renderHook(() => useAdoptOnSend(scheduled, send));

    await act(() => result.current("hello"));
    expect(adopt).toHaveBeenCalledWith("chat-1");
    expect(send).toHaveBeenCalled();
  });
});
