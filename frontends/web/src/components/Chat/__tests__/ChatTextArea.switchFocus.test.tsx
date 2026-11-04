/**
 * Switching chats puts the cursor in the new chat's composer, so typing goes
 * straight in — but only when that does not take focus from somewhere the
 * user is working, and never on touch (it would pop the on-screen keyboard).
 */

import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ChatTextArea } from "../ChatTextArea";
import { resetSwitchFocusForTests } from "../composerSwitchFocus";
import { useChatStore } from "../../../store/chatStore";

vi.mock("../../../store/chatNavigationStore", () => ({
  useChatNavigationStore: { getState: () => ({ navigateToChat: vi.fn() }) },
}));

/** What ChatInterface does: one composer per active chat, keyed on its id. */
function Composer({
  chatId,
  disabled = false,
}: {
  chatId?: string;
  disabled?: boolean;
}) {
  return (
    <ChatTextArea
      key={chatId}
      value=""
      onChange={() => {}}
      onSend={() => {}}
      chatId={chatId}
      disabled={disabled}
    />
  );
}

const composer = () => screen.getByTestId("chat-input");

function switchTo(chatId: string | null) {
  act(() => useChatStore.setState({ activeChatId: chatId }));
}

beforeEach(() => {
  useChatStore.setState({ activeChatId: null });
  resetSwitchFocusForTests();
});

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = "";
});

describe("switching chats focuses the composer", () => {
  it("focuses the new chat's composer when it mounts", () => {
    switchTo("chat-a");
    render(<Composer chatId="chat-a" />);
    expect(composer()).toHaveFocus();
  });

  it("follows each switch, including back from the new-chat view", () => {
    switchTo("chat-a");
    const { rerender } = render(<Composer chatId="chat-a" />);
    expect(composer()).toHaveFocus();

    switchTo("chat-b");
    rerender(<Composer chatId="chat-b" />);
    expect(composer()).toHaveFocus();

    // The new-chat view (no chat id) focuses itself; returning to a chat
    // already visited is still a switch.
    switchTo(null);
    rerender(<Composer />);
    composer().blur();
    switchTo("chat-a");
    rerender(<Composer chatId="chat-a" />);
    expect(composer()).toHaveFocus();
  });

  it("waits for a disabled composer to become ready", () => {
    switchTo("chat-a");
    const { rerender } = render(<Composer chatId="chat-a" disabled />);
    expect(composer()).not.toHaveFocus();

    rerender(<Composer chatId="chat-a" />);
    expect(composer()).toHaveFocus();
  });

  it("focuses once per switch, not on every remount in the same chat", () => {
    switchTo("chat-a");
    const { unmount } = render(<Composer chatId="chat-a" />);
    expect(composer()).toHaveFocus();
    unmount();

    // e.g. the composer hidden and re-shown behind a prompt.
    render(<Composer chatId="chat-a" />);
    expect(composer()).not.toHaveFocus();
  });

  it("takes focus from the click or shortcut that did the switch", () => {
    const row = document.createElement("button");
    document.body.appendChild(row);
    row.focus();

    switchTo("chat-a");
    render(<Composer chatId="chat-a" />);
    expect(composer()).toHaveFocus();
  });

  it("ignores a chat that is not the active one", () => {
    switchTo("chat-a");
    render(<Composer chatId="chat-other" />);
    expect(composer()).not.toHaveFocus();
  });

  it("never focuses a composer that belongs to no chat", () => {
    switchTo("chat-a");
    render(<Composer />);
    expect(composer()).not.toHaveFocus();
  });
});

describe("switching chats does not steal focus", () => {
  it("from another text field the user is typing in", () => {
    const field = document.createElement("input");
    document.body.appendChild(field);
    field.focus();

    switchTo("chat-a");
    const { rerender } = render(<Composer chatId="chat-a" disabled />);
    rerender(<Composer chatId="chat-a" />);
    expect(field).toHaveFocus();
  });

  it("from inside an open dialog", () => {
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    const button = document.createElement("button");
    dialog.appendChild(button);
    document.body.appendChild(dialog);
    button.focus();

    switchTo("chat-a");
    render(<Composer chatId="chat-a" />);
    expect(button).toHaveFocus();
  });

  it("on a touch-primary device, where it would pop the keyboard", () => {
    vi.stubGlobal(
      "matchMedia",
      (query: string) => ({ matches: query === "(pointer: coarse)" }) as MediaQueryList,
    );

    switchTo("chat-a");
    render(<Composer chatId="chat-a" />);
    expect(composer()).not.toHaveFocus();
  });

  it("decides once: a blocked switch does not grab focus later", () => {
    const field = document.createElement("input");
    document.body.appendChild(field);
    field.focus();

    switchTo("chat-a");
    const { unmount } = render(<Composer chatId="chat-a" />);
    expect(field).toHaveFocus();

    field.blur();
    unmount();
    render(<Composer chatId="chat-a" />);
    expect(composer()).not.toHaveFocus();
  });
});
