import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ChatTextArea } from "../ChatTextArea";
import type { CodeContext } from "../useCodeContexts";

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
  contexts,
  onRemoveContext,
}: {
  initial?: string;
  onSend?: () => void;
  isStreaming?: boolean;
  contexts?: CodeContext[];
  onRemoveContext?: (id: string) => void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <ChatTextArea
      value={value}
      onChange={setValue}
      onSend={onSend}
      isStreaming={isStreaming}
      contexts={contexts}
      onRemoveContext={onRemoveContext}
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

describe("code context chips", () => {
  const context: CodeContext = {
    id: "/src/app.ts:1-10",
    filePath: "/src/app.ts",
    fileName: "app.ts",
    startLine: 1,
    endLine: 10,
    language: "ts",
  };

  it("renders attached references above the text", () => {
    render(<Harness contexts={[context]} />);

    expect(screen.getByTestId("chat-input-contexts")).toBeInTheDocument();
    expect(screen.getByText(/app\.ts \(1-10\)/)).toBeInTheDocument();
  });

  it("shows no chip row when nothing is attached", () => {
    render(<Harness />);

    expect(screen.queryByTestId("chat-input-contexts")).not.toBeInTheDocument();
  });

  it("reports removal by id", async () => {
    const onRemoveContext = vi.fn();
    const user = userEvent.setup();
    render(<Harness contexts={[context]} onRemoveContext={onRemoveContext} />);

    await user.click(screen.getByTitle("Remove from chat"));

    expect(onRemoveContext).toHaveBeenCalledWith("/src/app.ts:1-10");
  });
});
