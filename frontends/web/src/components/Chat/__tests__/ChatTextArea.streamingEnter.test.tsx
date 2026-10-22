/**
 * Enter while streaming must always be handled deliberately -- never fall
 * through to the textarea's default newline insertion.
 *
 * Before the fix, `handleKeyDown` only called `preventDefault()` inside the
 * `!isStreaming` and `onQueue` branches. A composer with `isStreaming=true`
 * and no `onQueue` (e.g. BaseChatInput, the workflow-builder composer) hit
 * neither branch, so the `if` block exited without calling
 * `preventDefault()` and the browser's default action inserted a newline --
 * silently swallowing the keystroke as text instead of doing anything with
 * it.
 */

import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ChatTextArea } from "../ChatTextArea";

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
}));

vi.mock("../../../store/chatNavigationStore", () => ({
  useChatNavigationStore: { getState: () => ({ navigateToChat: vi.fn() }) },
}));

vi.mock("../../../lib/fileOpener", () => ({ useFileOpener: () => vi.fn() }));

function Harness({
  initial = "",
  onSend = vi.fn(),
  onQueue,
  onStop,
}: {
  initial?: string;
  onSend?: () => void;
  onQueue?: () => void;
  onStop?: () => void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <ChatTextArea
      value={value}
      onChange={setValue}
      onSend={onSend}
      onQueue={onQueue}
      onStop={onStop}
      isStreaming
    />
  );
}

const editor = () => screen.getByTestId("chat-input") as HTMLTextAreaElement;

describe("Enter while streaming never inserts a newline", () => {
  it("queues instead of inserting a newline when onQueue is provided", async () => {
    const onSend = vi.fn();
    const onQueue = vi.fn();
    const user = userEvent.setup();
    render(<Harness initial="check the file" onSend={onSend} onQueue={onQueue} />);

    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    await user.keyboard("{Enter}");

    expect(editor().value).toBe("check the file");
    expect(onQueue).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
  });

  it("falls back to onStop, not a newline, when there is no mailbox to queue into", async () => {
    const onSend = vi.fn();
    const onStop = vi.fn();
    const user = userEvent.setup();
    render(<Harness initial="stop me" onSend={onSend} onStop={onStop} />);

    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    await user.keyboard("{Enter}");

    expect(editor().value).toBe("stop me");
    expect(onStop).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
  });

  it("still never inserts a newline when neither onQueue nor onStop is provided", async () => {
    const onSend = vi.fn();
    const user = userEvent.setup();
    render(<Harness initial="nothing to do" onSend={onSend} />);

    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    await user.keyboard("{Enter}");

    // Nothing productive could be done, but the keystroke must still be
    // consumed deliberately -- not silently turned into a newline.
    expect(editor().value).toBe("nothing to do");
    expect(onSend).not.toHaveBeenCalled();
  });

  it("Shift+Enter still inserts a newline while streaming", async () => {
    const onQueue = vi.fn();
    const user = userEvent.setup();
    render(<Harness initial="hello" onQueue={onQueue} />);

    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    await user.keyboard("{Shift>}{Enter}{/Shift}");

    expect(editor().value).toBe("hello\n");
    expect(onQueue).not.toHaveBeenCalled();
  });
});
