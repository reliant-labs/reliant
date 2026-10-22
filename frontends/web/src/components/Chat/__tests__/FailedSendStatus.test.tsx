import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

// The "Not sent" line under a failed message: Retry sends the message again;
// Remove drops it. Retry is offered only while the message can still be resent
// from here (not after a reload).

const store = vi.hoisted(() => ({
  retryFailedSend: vi.fn(),
  discardFailedSend: vi.fn(),
  retryable: true,
}));
const toastError = vi.hoisted(() => vi.fn());

vi.mock("../../../store/chatStore", () => ({
  canRetryFailedSend: () => store.retryable,
  useChatStore: { getState: () => store },
}));
vi.mock("../../../lib/toast-manager", () => ({ toast: { error: toastError } }));

import { FailedSendStatus } from "../FailedSendStatus";

beforeEach(() => {
  store.retryFailedSend.mockReset();
  store.discardFailedSend.mockReset();
  store.retryable = true;
  toastError.mockReset();
});

describe("FailedSendStatus", () => {
  it("says the message was not sent, and retries it", async () => {
    store.retryFailedSend.mockResolvedValue(undefined);
    render(<FailedSendStatus chatId="c1" clientMessageId="cid-1" />);

    expect(screen.getByTestId("failed-send-status")).toHaveTextContent("Not sent");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(store.retryFailedSend).toHaveBeenCalledWith("c1", "cid-1"));
  });

  it("reports a retry that fails again", async () => {
    store.retryFailedSend.mockRejectedValue(new Error("still broken"));
    render(<FailedSendStatus chatId="c1" clientMessageId="cid-1" />);

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(toastError).toHaveBeenCalled());
  });

  it("removes the message", () => {
    render(<FailedSendStatus chatId="c1" clientMessageId="cid-1" />);
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    expect(store.discardFailedSend).toHaveBeenCalledWith("c1", "cid-1");
  });

  it("offers no Retry once the message cannot be resent", () => {
    store.retryable = false;
    render(<FailedSendStatus chatId="c1" clientMessageId="cid-1" />);
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    expect(screen.getByRole("button", { name: "Remove" })).toBeTruthy();
  });
});
