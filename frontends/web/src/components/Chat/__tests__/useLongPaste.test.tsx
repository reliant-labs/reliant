import { act, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { useRef, useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { isLongPaste, pasteCounts, useLongPaste } from "../useLongPaste";
import { useComposerUploads } from "../useComposerUploads";
import { LongPastePreview } from "../LongPastePreview";

const attach = vi.fn();
const remove = vi.fn();
function Harness({ session = "a" }: { session?: string }) {
  const ref = useRef<HTMLTextAreaElement>(null);
  const [input, setInput] = useState("before after");
  const queue = useComposerUploads(attach, remove, session);
  const paste = useLongPaste({ session, textarea: ref, input, setInput, queue, disabled: false, preferences: { enabled: true, characterThreshold: 5, lineThreshold: 3 } });
  return <><textarea aria-label="Composer" ref={ref} value={input} onChange={(e) => setInput(e.target.value)} /><input aria-label="Other" /><LongPastePreview paste={paste} /><button disabled={queue.uploads.length > 0 || paste.needsRecovery} onClick={paste.sent}>Send</button></>;
}
function paste(text: string, files: File[] = [], target = screen.getByLabelText("Composer")) {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", { value: { files, items: files.map(() => ({ kind: "file" })), getData: (type: string) => type === "text/plain" ? text : "<script>bad()</script>" } });
  act(() => { target.dispatchEvent(event); });
  return event;
}
beforeEach(() => { localStorage.clear(); attach.mockReset().mockResolvedValue({ id: "uploaded" }); remove.mockReset(); });
describe("long paste", () => {
  it("counts Unicode code points and CRLF lines; thresholds are inclusive", () => {
    expect(pasteCounts("😀\r\n𐐀\rA\n")).toEqual({ characters: 7, lines: 4 });
    expect(isLongPaste("12345", { enabled: true, characterThreshold: 5, lineThreshold: 100 })).toBe(true);
    expect(isLongPaste("1234", { enabled: true, characterThreshold: 5, lineThreshold: 100 })).toBe(false);
    expect(isLongPaste("12345", { enabled: false, characterThreshold: 5, lineThreshold: 100 })).toBe(false);
  });
  it("preserves exact file bytes, replaces selection, and immediate undo inserts inline", async () => {
    render(<Harness />);
    const el = screen.getByLabelText("Composer") as HTMLTextAreaElement;
    el.setSelectionRange(7, 12);
    const text = "😀\r\n<script>alert(1)</script>\n";
    expect(paste(text).defaultPrevented).toBe(true);
    expect(el.value).toBe("before ");
    const file = attach.mock.calls[0][1] as File;
    expect(file.type).toBe("text/plain");
    await act(async () => {
      const bytes = await new Promise<string>((resolve) => { const reader = new FileReader(); reader.onload = () => resolve(reader.result as string); reader.readAsText(file); });
      expect(bytes).toBe(text);
    });
    fireEvent.keyDown(el, { key: "z", ctrlKey: true });
    // DOM textarea normalizes CRLF; the uploaded file assertion above does not.
    expect(el.value).toBe("before " + text.replace(/\r\n/g, "\n"));
    await act(async () => {});
    expect(remove).toHaveBeenCalledWith("a", "uploaded");
  });
  it("does not intercept file-first, unrelated fields, short text, or shift-paste", () => {
    render(<Harness />);
    expect(paste("long text", [new File(["image"], "image.png")]).defaultPrevented).toBe(false);
    expect(paste("long text", [], screen.getByLabelText("Other")).defaultPrevented).toBe(false);
    expect(paste("tiny").defaultPrevented).toBe(false);
    fireEvent.keyDown(screen.getByLabelText("Composer"), { key: "V", metaKey: true, shiftKey: true });
    expect(paste("long text").defaultPrevented).toBe(false);
    expect(attach).not.toHaveBeenCalled();
  });
  it("handles Electron plain-text bridge inline with selection", () => {
    render(<Harness />);
    const el = screen.getByLabelText("Composer") as HTMLTextAreaElement;
    el.setSelectionRange(0, 6);
    const event = new CustomEvent("reliant:paste-plain-text", { bubbles: true, cancelable: true, detail: { text: "plain\n😀" } });
    act(() => { el.dispatchEvent(event); });
    expect(event.defaultPrevented).toBe(true);
    expect(el.value).toBe("plain\n😀 after");
    expect(attach).not.toHaveBeenCalled();
  });
  it("retains failed paste for edit/recovery and blocks sending", async () => {
    attach.mockRejectedValue(new Error("offline"));
    render(<Harness />);
    paste("original text");
    await act(async () => {});
    expect(screen.getByRole("alert").textContent).toContain("retained");
    expect((screen.getByText("Send") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByText("Edit"));
    fireEvent.change(screen.getByLabelText("Edit pasted text"), { target: { value: "edited 😀\n" } });
    fireEvent.click(screen.getByText("Save edit"));
    fireEvent.click(screen.getByText("Insert as text"));
    expect((screen.getByLabelText("Composer") as HTMLTextAreaElement).value).toContain("edited 😀\n");
  });
  it("restores raw drafts after remount without persisting upload IDs or duplicating attachments", async () => {
    const first = render(<Harness />);
    paste("saved paste");
    await act(async () => {});
    expect(localStorage.getItem("reliant:long-paste:v1:a")).not.toContain("uploaded");
    first.unmount();
    expect(remove).toHaveBeenCalledWith("a", "uploaded");
    render(<Harness />);
    expect((screen.getByText("Send") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByText("Upload attachment"));
    await act(async () => {});
    expect(attach).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByText("Send"));
    expect(localStorage.getItem("reliant:long-paste:v1:a")).toBeNull();
  });
  it("keeps sessions separate and removes late success after recovery", async () => {
    let resolve!: (v: { id: string }) => void;
    attach.mockImplementation(() => new Promise((r) => { resolve = r; }));
    const view = render(<Harness />);
    paste("session a");
    view.rerender(<Harness session="b" />);
    expect(screen.queryByText("Insert as text")).toBeNull();
    view.rerender(<Harness session="a" />);
    fireEvent.click(screen.getByText("Insert as text"));
    await act(async () => { resolve({ id: "late" }); });
    expect(remove).toHaveBeenCalledWith("a", "late");
  });
  it("transfers submitted upload ownership before unmount and restores raw text on rejection", async () => {
    const textarea = document.createElement("textarea");
    document.body.appendChild(textarea);
    const ref = { current: textarea };
    const hook = renderHook(() => {
      const queue = useComposerUploads(attach, remove, "send");
      return useLongPaste({ session: "send", textarea: ref, input: "", setInput: vi.fn(), preferences: { enabled: true, characterThreshold: 5, lineThreshold: 3 }, queue, disabled: false });
    });
    paste("send content", [], textarea);
    await act(async () => {});
    const submitted = hook.result.current;
    act(() => submitted.submitting());
    hook.unmount();
    expect(remove).not.toHaveBeenCalled();
    // A rejection after unmount must still persist recoverable text without IDs.
    act(() => submitted.sendFailed());
    expect(JSON.parse(localStorage.getItem("reliant:long-paste:v1:send")!)).toEqual([{ id: expect.any(Number), text: "send content" }]);
    textarea.remove();
  });

  it("leaves oversize paste inline with a visible explanation", () => {
    render(<Harness />);
    expect(paste("x".repeat(1024 * 1024)).defaultPrevented).toBe(false);
    expect(screen.getByRole("status").textContent).toContain("kept as text");
    expect(attach).not.toHaveBeenCalled();
  });
});
