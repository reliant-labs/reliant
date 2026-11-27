// Behaviour of markdown_editor in a real DOM (jsdom) with real React event
// delegation: toolbar and shortcut formatting, list continuation, limits and
// the safe preview. Run by TestComponentBehaviour.
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import MarkdownEditor from "./markdown_editor";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
});

function render(ui: React.ReactElement) {
  act(() => root.render(ui));
}

const textarea = () => document.querySelector("textarea") as HTMLTextAreaElement;
const button = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
    (b.getAttribute("aria-label") ?? b.textContent ?? "").startsWith(label),
  ) as HTMLButtonElement;

function select(start: number, end: number) {
  textarea().focus();
  textarea().setSelectionRange(start, end);
}

function key(init: KeyboardEventInit) {
  act(() => {
    textarea().dispatchEvent(
      new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }),
    );
  });
}

test("the toolbar wraps the selection and keeps it selected", () => {
  const onChange = vi.fn();
  render(<MarkdownEditor ariaLabel="Post" defaultValue="hello world" onChange={onChange} />);
  select(0, 5);
  act(() => button("Bold").click());
  expect(textarea().value).toBe("**hello** world");
  expect(onChange).toHaveBeenLastCalledWith("**hello** world");
  expect([textarea().selectionStart, textarea().selectionEnd]).toEqual([2, 7]);
  // Bold again on the same selection unwraps it.
  act(() => button("Bold").click());
  expect(textarea().value).toBe("hello world");
});

test("shortcuts format without the toolbar", () => {
  render(<MarkdownEditor ariaLabel="Post" defaultValue="see docs" />);
  select(4, 8);
  key({ key: "k", ctrlKey: true });
  expect(textarea().value).toBe("see [docs](url)");
  // The url placeholder is selected, ready to be typed over.
  expect(textarea().value.slice(textarea().selectionStart, textarea().selectionEnd)).toBe("url");

  select(0, 0);
  key({ key: "*", code: "Digit8", ctrlKey: true, shiftKey: true });
  expect(textarea().value).toBe("- see [docs](url)");
});

test("Enter continues a list and an empty item ends it", () => {
  render(<MarkdownEditor ariaLabel="Post" defaultValue="1. one" />);
  select(6, 6);
  key({ key: "Enter" });
  expect(textarea().value).toBe("1. one\n2. ");
  select(10, 10);
  key({ key: "Enter" });
  expect(textarea().value).toBe("1. one\n");
});

test("a controlled editor reports changes and renders only the parent's value", () => {
  const onChange = vi.fn();
  render(<MarkdownEditor ariaLabel="Post" value="fixed" onChange={onChange} />);
  select(0, 5);
  act(() => button("Italic").click());
  expect(onChange).toHaveBeenLastCalledWith("_fixed_");
  expect(textarea().value).toBe("fixed");
});

test("limits count graphemes and warn once exceeded", () => {
  const text = "a".repeat(299) + "👍🏽";
  render(
    <MarkdownEditor
      ariaLabel="Post"
      defaultValue={text}
      limits={[{ label: "Bluesky", max: 300 }]}
    />,
  );
  expect(document.body.textContent).toContain("Bluesky 300/300");
  expect(document.querySelector('[aria-live="polite"]')?.textContent).toBe("");
  expect(textarea().getAttribute("aria-invalid")).toBeNull();
});

test("an over-limit draft is flagged and announced", () => {
  render(
    <MarkdownEditor
      ariaLabel="Post"
      defaultValue={"b".repeat(302)}
      limits={[{ label: "Bluesky", max: 300 }]}
    />,
  );
  expect(document.body.textContent).toContain("2 over");
  expect(document.querySelector('[aria-live="polite"]')?.textContent).toBe(
    "Over the Bluesky limit",
  );
  expect(textarea().getAttribute("aria-invalid")).toBe("true");
});

test("the preview renders markdown as elements and drops unsafe links", () => {
  render(
    <MarkdownEditor
      ariaLabel="Post"
      defaultValue={
        "# Title\n\n**bold** and [ok](https://x.dev) and [bad](javascript:alert(1))\n\n- a\n- b"
      }
    />,
  );
  act(() => button("Preview").click());
  const panel = document.querySelector('[role="tabpanel"]:not([hidden])') as HTMLElement;
  expect(panel.querySelector("h1")?.textContent).toBe("Title");
  expect(panel.querySelector("strong")?.textContent).toBe("bold");
  const links = [...panel.querySelectorAll("a")];
  expect(links.map((a) => a.getAttribute("href"))).toEqual(["https://x.dev"]);
  expect(panel.textContent).toContain("bad");
  expect(panel.querySelectorAll("li")).toHaveLength(2);
  expect(button("Preview").getAttribute("aria-selected")).toBe("true");
});
