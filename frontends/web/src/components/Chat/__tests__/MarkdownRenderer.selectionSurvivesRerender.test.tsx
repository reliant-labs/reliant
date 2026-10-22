/**
 * Re-rendering a settled message must not rebuild its DOM.
 *
 * MarkdownRenderer hands react-markdown a `components` map whose entries are
 * component functions — React uses them as the element TYPES for <p>, <li>,
 * <code>, … When that map was rebuilt on every render, each re-render gave
 * React new types, so it unmounted and remounted the message's entire DOM.
 *
 * That destroyed any text selection anchored in the message: the anchor node
 * was removed mid-drag, so the highlight collapsed or jumped onto unrelated
 * text. Streaming re-renders the visible messages many times a second, which
 * is why the bug showed up as "the highlight moves while I hold still" during
 * a reply. Measured on the real timeline: the paragraph under the cursor was
 * removed ~15ms after mousedown while a reply streamed.
 */
import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MarkdownRenderer } from "../MarkdownRenderer";

vi.mock("../../../lib/open-link", () => ({ openLink: vi.fn() }));

const CONTENT = "First paragraph of a settled message.\n\n- a list item\n\nAnd `inline code` too.";

describe("MarkdownRenderer re-render", () => {
  it("keeps the same text nodes when a settled message re-renders", () => {
    const { container, rerender } = render(<MarkdownRenderer content={CONTENT} worktreeId="wt-1" />);
    const paragraph = container.querySelector("p");
    const textNode = paragraph?.firstChild;
    expect(textNode?.textContent).toContain("First paragraph");

    // An unrelated parent re-render: same props, new element.
    rerender(<MarkdownRenderer content={CONTENT} worktreeId="wt-1" />);

    expect(container.querySelector("p")).toBe(paragraph);
    expect(container.querySelector("p")?.firstChild).toBe(textNode);
    expect(textNode?.isConnected).toBe(true);
  });

  it("keeps a selection anchored in the message across a re-render", () => {
    const { container, rerender } = render(<MarkdownRenderer content={CONTENT} worktreeId="wt-1" />);
    const textNode = container.querySelector("p")?.firstChild as Text;
    const selection = window.getSelection();
    selection?.setBaseAndExtent(textNode, 0, textNode, 5);
    expect(selection?.toString()).toBe("First");

    rerender(<MarkdownRenderer content={CONTENT} worktreeId="wt-1" />);

    expect(selection?.anchorNode).toBe(textNode);
    expect(selection?.toString()).toBe("First");
  });
});
