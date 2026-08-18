/**
 * The keyboard race: the streaming flag is sampled twice -- once by
 * ChatTextArea's handleKeyDown (to decide send vs. queue vs. newline) and
 * again, independently, by ChatInput's handleSend (`effectiveStreaming`,
 * derived from isDiscussMode / hasPendingQuestion / isStreaming, any of
 * which can change between the two reads because hasPendingQuestion is a
 * React Query subscription that can settle at any moment).
 *
 * Before the fix, `handleSend` had no else branch: if `willSend` was false
 * because streaming had started in the gap between the two samples, the
 * keystroke vanished -- no send, no queue, no error.
 *
 * This harness reproduces the two-samples-disagree shape directly (a ref
 * ChatTextArea's onSend callback reads independently of the isStreaming prop
 * it based its own decision on), which is the fastest, most deterministic
 * way to force the exact disagreement window described in the bug without
 * depending on real React Query timing in a test environment. It exercises
 * the real ChatTextArea component and a handleSend implementation with the
 * same shape as ChatInput.handleSend (willSend check, else-queue fallback).
 */

import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef, useState } from "react";
import { ChatTextArea } from "../ChatTextArea";

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
}));

vi.mock("../../../store/chatNavigationStore", () => ({
  useChatNavigationStore: { getState: () => ({ navigateToChat: vi.fn() }) },
}));

vi.mock("../../../lib/fileOpener", () => ({ useFileOpener: () => vi.fn() }));

const editor = () => screen.getByTestId("chat-input") as HTMLTextAreaElement;

/**
 * Mirrors the real shape: ChatTextArea decides via its own `isStreaming`
 * prop; ChatInput's handleSend independently re-derives streaming state and
 * must never silently no-op if that second read disagrees with the first.
 */
function RaceHarness({
  streamingAtKeydown,
  streamingAtHandleSend,
  onSend,
  onQueue,
}: {
  streamingAtKeydown: boolean;
  streamingAtHandleSend: boolean;
  onSend: () => void;
  onQueue: () => void;
}) {
  const [value, setValue] = useState("check the migration file");
  // The second, independent sample -- e.g. effectiveStreaming re-derived
  // from a React Query subscription that settled after ChatTextArea already
  // made its decision.
  const secondSample = useRef(streamingAtHandleSend);
  secondSample.current = streamingAtHandleSend;

  const handleSend = () => {
    const willSend = value.trim().length > 0 && !secondSample.current;
    if (willSend) {
      onSend();
    } else if (value.trim().length > 0 && secondSample.current) {
      // The fix: never fall off the end silently.
      onQueue();
    }
  };

  return (
    <ChatTextArea
      value={value}
      onChange={setValue}
      onSend={handleSend}
      onQueue={onQueue}
      isStreaming={streamingAtKeydown}
    />
  );
}

describe("streaming-flag race between keydown and the send handler", () => {
  it("queues rather than silently dropping the keystroke when the second sample disagrees", async () => {
    const onSend = vi.fn();
    const onQueue = vi.fn();
    const user = userEvent.setup();

    // ChatTextArea's keydown handler samples isStreaming=false and decides
    // to call onSend(). By the time onSend (handleSend) runs, the
    // independently-derived second sample has flipped to true.
    render(
      <RaceHarness
        streamingAtKeydown={false}
        streamingAtHandleSend={true}
        onSend={onSend}
        onQueue={onQueue}
      />,
    );

    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    await user.keyboard("{Enter}");

    // The message must be queued, not sent as a new turn and not lost.
    expect(onQueue).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
    // Enter must still be consumed -- no newline inserted.
    expect(el.value).toBe("check the migration file");
  });

  it("sends normally when both samples agree the agent is idle", async () => {
    const onSend = vi.fn();
    const onQueue = vi.fn();
    const user = userEvent.setup();

    render(
      <RaceHarness
        streamingAtKeydown={false}
        streamingAtHandleSend={false}
        onSend={onSend}
        onQueue={onQueue}
      />,
    );

    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    await user.keyboard("{Enter}");

    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onQueue).not.toHaveBeenCalled();
  });
});
