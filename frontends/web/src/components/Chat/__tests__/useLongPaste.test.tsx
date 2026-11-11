import { act, fireEvent, render, screen } from "@testing-library/react";
import { useRef, useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  LONG_PASTE_MIN_CHARACTERS,
  LONG_PASTE_MIN_LINES,
  isLongPaste,
  legacyPasteDraftKey,
  legacyPasteDraftKeys,
  migrateLegacyPasteDrafts,
  pastedTextFilename,
  useLongPaste,
} from "../useLongPaste";

const attach = vi.fn();
function Harness() {
  const ref = useRef<HTMLTextAreaElement>(null);
  const [input, setInput] = useState("before after");
  useLongPaste({ textarea: ref, input, setInput, attach, disabled: false });
  return (
    <>
      <textarea aria-label="Composer" ref={ref} value={input} onChange={(e) => setInput(e.target.value)} />
      <input aria-label="Other" />
    </>
  );
}
function paste(text: string, files: File[] = [], target = screen.getByLabelText("Composer")) {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: { files, items: files.map(() => ({ kind: "file" })), getData: (type: string) => (type === "text/plain" ? text : "<b>html</b>") },
  });
  act(() => { target.dispatchEvent(event); });
  return event;
}
const long = "line\n".repeat(LONG_PASTE_MIN_LINES);

beforeEach(() => {
  localStorage.clear();
  attach.mockReset();
});

describe("isLongPaste", () => {
  it("counts code points and CRLF/CR/LF lines; both thresholds are inclusive", () => {
    expect(isLongPaste("a".repeat(LONG_PASTE_MIN_CHARACTERS))).toBe(true);
    expect(isLongPaste("a".repeat(LONG_PASTE_MIN_CHARACTERS - 1))).toBe(false);
    // 4999 astral code points are 9998 UTF-16 units: still short.
    expect(isLongPaste("😀".repeat(LONG_PASTE_MIN_CHARACTERS - 1))).toBe(false);
    expect(isLongPaste("x\r\n".repeat(LONG_PASTE_MIN_LINES - 1))).toBe(true);
    expect(isLongPaste("x\r".repeat(LONG_PASTE_MIN_LINES - 2))).toBe(false);
  });
});

describe("pastedTextFilename", () => {
  it("numbers from (2), filling the first gap", () => {
    expect(pastedTextFilename([])).toBe("pasted-text.txt");
    expect(pastedTextFilename(["pasted-text.txt"])).toBe("pasted-text(2).txt");
    expect(pastedTextFilename(["pasted-text.txt", "pasted-text(2).txt", "notes.md"])).toBe("pasted-text(3).txt");
    expect(pastedTextFilename(["pasted-text.txt", "pasted-text(3).txt"])).toBe("pasted-text(2).txt");
    expect(pastedTextFilename(["pasted-text(2).txt"])).toBe("pasted-text.txt");
  });
});

describe("useLongPaste", () => {
  it("attaches a long paste with its exact text and drops the selection it replaced", () => {
    render(<Harness />);
    const el = screen.getByLabelText("Composer") as HTMLTextAreaElement;
    el.setSelectionRange(7, 12);
    const text = "😀\r\n<script>alert(1)</script>\n" + long;
    expect(paste(text).defaultPrevented).toBe(true);
    expect(attach).toHaveBeenCalledWith(text);
    expect(el.value).toBe("before ");
  });

  it("leaves files, other fields, short text and Shift+paste to the browser", () => {
    render(<Harness />);
    expect(paste(long, [new File(["image"], "image.png")]).defaultPrevented).toBe(false);
    expect(paste(long, [], screen.getByLabelText("Other")).defaultPrevented).toBe(false);
    expect(paste("tiny").defaultPrevented).toBe(false);
    fireEvent.keyDown(screen.getByLabelText("Composer"), { key: "V", metaKey: true, shiftKey: true });
    expect(paste(long).defaultPrevented).toBe(false);
    expect(attach).not.toHaveBeenCalled();
    // The bypass is one paste wide.
    expect(paste(long).defaultPrevented).toBe(true);
  });

  it("inserts the Electron plain-text bridge inline over the selection", () => {
    render(<Harness />);
    const el = screen.getByLabelText("Composer") as HTMLTextAreaElement;
    el.setSelectionRange(0, 6);
    const event = new CustomEvent("reliant:paste-plain-text", { cancelable: true, detail: { text: long } });
    act(() => { el.dispatchEvent(event); });
    expect(event.defaultPrevented).toBe(true);
    expect(el.value).toBe(long + " after");
    expect(attach).not.toHaveBeenCalled();
  });
});

describe("legacyPasteDraftKeys", () => {
  it("reads only the signed-in user's pastes in the current project", () => {
    const mine = legacyPasteDraftKeys({ userId: "u1", projectId: "p1", sessionId: "new-chat:project:p1", newChat: true });
    expect(mine).toEqual([
      legacyPasteDraftKey("u1", "p1", "new-chat:project:p1"),
      legacyPasteDraftKey("u1", "p1", "temp"),
    ]);
    // Another account's or project's old "temp" slot is a different key, never one of ours.
    expect(mine).not.toContain(legacyPasteDraftKey("u2", "p1", "temp"));
    expect(mine).not.toContain(legacyPasteDraftKey("u1", "p2", "temp"));
    // An existing chat never takes the new-chat slot.
    expect(legacyPasteDraftKeys({ userId: "u1", projectId: "p1", sessionId: "chat-1", newChat: false }))
      .toEqual([legacyPasteDraftKey("u1", "p1", "chat-1")]);
  });

  it("reads nothing while the user or project is unknown", () => {
    expect(legacyPasteDraftKeys({ userId: undefined, projectId: "p1", sessionId: "temp", newChat: true })).toEqual([]);
    expect(legacyPasteDraftKeys({ userId: "u1", projectId: undefined, sessionId: "temp", newChat: true })).toEqual([]);
  });

  it("leaves another identity's stored paste untouched when switching user or project", async () => {
    const theirs = legacyPasteDraftKey("u-old", "p-old", "temp");
    localStorage.setItem(theirs, JSON.stringify([{ id: 1, text: "someone else's draft" }]));
    const upload = vi.fn().mockResolvedValue("att");
    await migrateLegacyPasteDrafts(
      legacyPasteDraftKeys({ userId: "u-new", projectId: "p-new", sessionId: "new-chat:project:p-new", newChat: true }),
      upload,
      () => true,
    );
    expect(upload).not.toHaveBeenCalled();
    expect(JSON.parse(localStorage.getItem(theirs)!)).toEqual([{ id: 1, text: "someone else's draft" }]);
  });
});

describe("migrateLegacyPasteDrafts", () => {
  it("uploads each stored v1.10 paste once and drops the entry", async () => {
    const key = legacyPasteDraftKey("u", "p", "chat-1");
    localStorage.setItem(key, JSON.stringify([{ id: 1, text: "first" }, { id: 2, text: "" }, { id: 3, text: "second" }]));
    const upload = vi.fn(async (text: string) => `att-${text}`);
    await migrateLegacyPasteDrafts([key, legacyPasteDraftKey("u", "p", "absent")], upload, () => true);
    expect(upload.mock.calls).toEqual([["first"], ["second"]]);
    expect(localStorage.getItem(key)).toBeNull();
  });

  it("keeps what failed to upload for the next mount", async () => {
    const key = legacyPasteDraftKey("u", undefined, "temp");
    localStorage.setItem(key, JSON.stringify([{ id: 1, text: "ok" }, { id: 2, text: "fails" }]));
    const upload = vi.fn().mockResolvedValueOnce("att-ok").mockRejectedValueOnce(new Error("offline"));
    await migrateLegacyPasteDrafts([key], upload, () => true);
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual([{ id: 2, text: "fails" }]);
  });

  it("keeps the text until the uploaded attachment is in the persisted draft, without uploading it twice", async () => {
    const key = legacyPasteDraftKey("u", "p", "chat-2");
    localStorage.setItem(key, JSON.stringify([{ id: 7, text: "only copy" }]));
    const upload = vi.fn().mockResolvedValue("att-7");
    let stored = false;
    const persisted = vi.fn(() => stored);

    // Upload succeeds, but the draft write was refused: the text is the only copy a reload has.
    await migrateLegacyPasteDrafts([key], upload, persisted);
    expect(persisted).toHaveBeenCalledWith("att-7");
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual([{ id: 7, text: "only copy" }]);

    // A later mount on the same page, once the draft is stored: confirm and drop, no second upload.
    stored = true;
    await migrateLegacyPasteDrafts([key], upload, persisted);
    expect(upload).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem(key)).toBeNull();
  });
});
