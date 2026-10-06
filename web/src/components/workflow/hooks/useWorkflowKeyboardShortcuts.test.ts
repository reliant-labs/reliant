/**
 * Escape in the builder (research/WORKFLOW_EDITOR_UX_REVIEW.md issue 10):
 * it closes the innermost open thing and never leaves the builder. Pressing
 * Escape to close the validation popover used to navigate to the Library.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, renderHook } from "@testing-library/react";

import { useWorkflowKeyboardShortcuts, type UseWorkflowKeyboardShortcutsArgs } from "./useWorkflowKeyboardShortcuts";

function setup(overrides: Partial<UseWorkflowKeyboardShortcutsArgs> & Record<string, unknown> = {}) {
  const args = {
    onUndo: vi.fn(),
    onRedo: vi.fn(),
    hasOpenPanel: false,
    closePanels: vi.fn(),
    hasTestRunPanel: false,
    closeTestRunPanel: vi.fn(),
    ...overrides,
  };
  renderHook(() => useWorkflowKeyboardShortcuts(args as UseWorkflowKeyboardShortcutsArgs));
  return args;
}

const pressEscape = (target: EventTarget = document.body) => fireEvent.keyDown(target, { key: "Escape" });

afterEach(() => {
  document.body.innerHTML = "";
});

describe("Escape in the workflow builder", () => {
  it("never navigates away when nothing is open", () => {
    // The old hook took a back handler and called it here.
    const onEscape = vi.fn();
    const args = setup({ onEscape });
    pressEscape();
    expect(onEscape).not.toHaveBeenCalled();
    expect(args.closePanels).not.toHaveBeenCalled();
  });

  it("closes an open side panel", () => {
    const args = setup({ hasOpenPanel: true });
    pressEscape();
    expect(args.closePanels).toHaveBeenCalledTimes(1);
  });

  it("closes the Test run panel only once the side panels are closed", () => {
    const withPanel = setup({ hasOpenPanel: true, hasTestRunPanel: true });
    pressEscape();
    expect(withPanel.closePanels).toHaveBeenCalledTimes(1);
    expect(withPanel.closeTestRunPanel).not.toHaveBeenCalled();
  });

  it("leaves Escape to an open popover, dialog or menu instead of closing the panel behind it", () => {
    for (const markup of [
      '<div data-escape-layer role="dialog" aria-label="Problems"></div>',
      '<div role="dialog" aria-modal="true"></div>',
      '<div role="menu"></div>',
      '<div role="listbox"></div>',
    ]) {
      document.body.innerHTML = markup;
      const onEscape = vi.fn();
      const args = setup({ hasOpenPanel: true, onEscape });
      pressEscape();
      expect(args.closePanels, markup).not.toHaveBeenCalled();
      expect(onEscape, markup).not.toHaveBeenCalled();
    }
  });

  it("leaves Escape to the field being typed in", () => {
    const input = document.createElement("input");
    document.body.appendChild(input);
    const args = setup({ hasOpenPanel: true });
    pressEscape(input);
    expect(args.closePanels).not.toHaveBeenCalled();
  });
});
