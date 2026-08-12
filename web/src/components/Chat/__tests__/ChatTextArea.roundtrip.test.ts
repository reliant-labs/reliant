import { describe, it, expect } from "vitest";
import { __chatTextAreaTestUtils } from "../ChatTextArea";

const { serializeFromDom } = __chatTextAreaTestUtils;

/**
 * Mirrors the DOM-rebuild branch of ChatTextArea's value->DOM layout effect.
 * Kept in lockstep with that code by hand; these cases exist to prove the
 * rebuild and the serializer agree on what a given value looks like.
 */
function rebuildFromValue(value: string): HTMLElement {
  const el = document.createElement("div");
  const lines = value.split("\n");
  for (let i = 0; i < lines.length; i++) {
    el.appendChild(document.createTextNode(lines[i]));
    if (i < lines.length - 1) {
      el.appendChild(document.createElement("br"));
    }
  }
  if (value.endsWith("\n")) {
    el.appendChild(document.createElement("br"));
  }
  return el;
}

/**
 * A block wrapper opens a line as well as closing one.
 *
 * execCommand('insertText') — the paste path — wraps everything after the
 * first line in <div>s, giving `text<div>more</div>`. Serializing that as
 * "textmore" cost the user a newline: the value no longer matched the DOM, so
 * the next keystroke rebuilt the composer from the shorter value and the
 * caret jumped, which is what "won't let me add a new line after a paste"
 * looked like from the outside.
 */
describe("serializeFromDom block boundaries", () => {
  const cases: Array<[string, string]> = [
    ["a<div>b</div>", "a\nb"],
    ["line1<div>line2</div><div>line3</div>", "line1\nline2\nline3"],
    ["<div>a</div><div>b</div>", "a\nb"],
    ["a<div>b<br></div>", "a\nb"],
    // An explicit <br> already opened the line; the block must not add a second.
    ["a<br><div>b</div>", "a\nb"],
    // A blank line between two pasted paragraphs survives.
    ["a<div><br></div><div>b</div>", "a\n\nb"],
  ];

  for (const [html, expected] of cases) {
    it(`serializes ${html} as ${JSON.stringify(expected)}`, () => {
      const el = document.createElement("div");
      el.innerHTML = html;
      expect(serializeFromDom(el)).toBe(expected);
    });
  }
});

describe("ChatTextArea value <-> DOM round trip", () => {
  const cases = [
    "hello",
    "line 1\nline 2",
    "trailing newline\n",
    "two trailing newlines\n\n",
    "a\n\nb",
  ];

  for (const value of cases) {
    it(`round-trips ${JSON.stringify(value)}`, () => {
      const el = rebuildFromValue(value);
      expect(serializeFromDom(el)).toBe(value);
    });
  }
});
