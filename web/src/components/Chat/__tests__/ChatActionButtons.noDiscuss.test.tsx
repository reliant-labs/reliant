import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChatActionButtons } from "../ChatActionButtons";

// Legacy discuss props are passed untyped so the test also compiles (and fails
// meaningfully) against a tree that still declares them.
const legacyDiscussProps = {
  isPaused: true,
  isDiscussMode: false,
  onToggleDiscuss: vi.fn(),
} as Record<string, unknown>;

describe("ChatActionButtons while the workflow is paused", () => {
  it("does not render a Discuss button", () => {
    render(
      <ChatActionButtons
        onSend={vi.fn()}
        canSend
        isStreaming={false}
        disabled={false}
        onAttach={vi.fn()}
        uploading={false}
        {...legacyDiscussProps}
      />,
    );
    expect(screen.queryByText(/discuss/i)).toBeNull();
    expect(screen.queryByRole("button", { name: /discuss/i })).toBeNull();
    expect(screen.getByTestId("send-button")).toBeTruthy();
  });
});
