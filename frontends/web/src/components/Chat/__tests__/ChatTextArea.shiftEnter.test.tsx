import { afterEach, describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
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

/** Controlled exactly like ChatInput drives it. */
function Harness({
  initial = "",
  onSend = vi.fn(),
  isStreaming = false,
  onQueue,
}: {
  initial?: string;
  onSend?: () => void;
  isStreaming?: boolean;
  onQueue?: () => void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <ChatTextArea
      value={value}
      onChange={setValue}
      onSend={onSend}
      isStreaming={isStreaming}
      onQueue={onQueue}
    />
  );
}

const editor = () => screen.getByTestId("chat-input") as HTMLTextAreaElement;

describe("newlines", () => {
  /**
   * The composer used to be a contentEditable div that relied on the browser's
   * default editing action to insert a <br>. When the caret sat between element
   * children — where a paste leaves it — that default did nothing and the line
   * break was silently lost. A textarea has no such failure mode.
   */
  it("Shift+Enter inserts a newline rather than sending", async () => {
    const onSend = vi.fn();
    const user = userEvent.setup();
    render(<Harness initial="hello" onSend={onSend} />);

    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    await user.keyboard("{Shift>}{Enter}{/Shift}");

    expect(el.value).toBe("hello\n");
    expect(onSend).not.toHaveBeenCalled();
  });

  it("keeps inserting newlines after text that already ends in one", async () => {
    const user = userEvent.setup();
    render(<Harness initial={"line one\nline two\n"} />);

    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    await user.keyboard("{Shift>}{Enter}{/Shift}");

    expect(el.value).toBe("line one\nline two\n\n");
  });

  it("preserves a multi-line paste verbatim", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    const pasted = "\n\tfirst\n\nlast line\n";
    const el = editor();
    el.focus();
    await user.paste(pasted);

    expect(el.value).toBe(pasted);
  });

  it("sends on plain Enter", async () => {
    const onSend = vi.fn();
    const user = userEvent.setup();
    render(<Harness initial="ship it" onSend={onSend} />);

    editor().focus();
    await user.keyboard("{Enter}");

    expect(onSend).toHaveBeenCalledTimes(1);
  });

  it("does not send on Enter that commits an IME composition", () => {
    const onSend = vi.fn();
    render(<Harness initial="にほんご" onSend={onSend} />);

    fireEvent.keyDown(editor(), { key: "Enter", isComposing: true });

    expect(onSend).not.toHaveBeenCalled();
  });
});

/** The primary pointer, as `(pointer: coarse)` reports it. */
function primaryPointer(kind: "coarse" | "fine") {
  vi.stubGlobal(
    "matchMedia",
    (query: string) =>
      ({
        matches: kind === "coarse" && query === "(pointer: coarse)",
        addEventListener: () => {},
        removeEventListener: () => {},
      }) as unknown as MediaQueryList,
  );
}

describe("which key sends (design doc D2)", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("fine pointer: Enter sends, Cmd+Enter does not", () => {
    primaryPointer("fine");
    const onSend = vi.fn();
    render(<Harness initial="ship it" onSend={onSend} />);

    fireEvent.keyDown(editor(), { key: "Enter", metaKey: true });
    expect(onSend).not.toHaveBeenCalled();
    fireEvent.keyDown(editor(), { key: "Enter" });
    expect(onSend).toHaveBeenCalledTimes(1);
  });

  it("coarse pointer: Return inserts a newline instead of sending", async () => {
    primaryPointer("coarse");
    const onSend = vi.fn();
    const user = userEvent.setup();
    render(<Harness initial="half a thought" onSend={onSend} />);

    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    await user.keyboard("{Enter}");

    expect(el.value).toBe("half a thought\n");
    expect(onSend).not.toHaveBeenCalled();
  });

  it.each([{ metaKey: true }, { ctrlKey: true }])("coarse pointer: %o+Enter sends", (modifier) => {
    primaryPointer("coarse");
    const onSend = vi.fn();
    render(<Harness initial="ship it" onSend={onSend} />);

    fireEvent.keyDown(editor(), { key: "Enter", ...modifier });
    expect(onSend).toHaveBeenCalledTimes(1);
  });

  it("coarse pointer: Cmd+Enter queues while the agent runs", () => {
    primaryPointer("coarse");
    const onSend = vi.fn();
    const onQueue = vi.fn();
    render(<Harness initial="also do this" onSend={onSend} onQueue={onQueue} isStreaming />);

    fireEvent.keyDown(editor(), { key: "Enter" });
    expect(onQueue).not.toHaveBeenCalled();
    fireEvent.keyDown(editor(), { key: "Enter", metaKey: true });
    expect(onQueue).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
  });

  it("coarse pointer: the newline shortcut (dispatched Cmd+Enter) sends instead", () => {
    primaryPointer("coarse");
    const onSend = vi.fn();
    render(<Harness initial="ship it" onSend={onSend} />);

    editor().focus();
    fireEvent(window, new CustomEvent("composer-insert-newline"));
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(editor().value).toBe("ship it");
  });

  it("fine pointer: the newline shortcut inserts a newline", () => {
    primaryPointer("fine");
    const onSend = vi.fn();
    render(<Harness initial="ship it" onSend={onSend} />);

    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    fireEvent(window, new CustomEvent("composer-insert-newline"));
    expect(onSend).not.toHaveBeenCalled();
    expect(el.value).toBe("ship it\n");
  });
});
