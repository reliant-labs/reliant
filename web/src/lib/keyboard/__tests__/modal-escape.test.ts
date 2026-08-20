/**
 * Escape while a modal is open, end to end through DOM context detection.
 *
 * registry.test and bindings.test resolve against a hand-supplied context list.
 * The bug this file guards lived in the step BEFORE that: the modal is detected
 * from the DOM via `data-modal-open`, and if that detection does not run, the
 * dispatcher never enters the `modal` context and Escape falls through to
 * stopStreaming — which pauses the chat sitting behind the modal.
 *
 * So these drive the real detectActiveContexts against a real DOM node.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { defaultShortcuts } from "../../../store/shortcutsData.generated";
import { parseBinding } from "../chord";
import { ShortcutRegistry, type ResolvedShortcut } from "../registry";
import { createDispatcher } from "../dispatcher";

function buildRegistry(): ShortcutRegistry {
  const resolved: ResolvedShortcut[] = Object.values(defaultShortcuts).map(
    (shortcut) => ({
      id: shortcut.id,
      handler: shortcut.handler,
      binding: parseBinding(shortcut.defaultBinding, true),
      context: shortcut.context,
      allowInInput: shortcut.allowInInput,
      passthrough: shortcut.passthrough,
    }),
  );
  return new ShortcutRegistry(resolved);
}

/** Mount a modal the way the app's overlays do, and focus its search input. */
function openModal(): HTMLInputElement {
  const overlay = document.createElement("div");
  overlay.setAttribute("data-modal-open", "true");
  const input = document.createElement("input");
  overlay.appendChild(input);
  document.body.appendChild(overlay);
  input.focus();
  return input;
}

function pressEscape(target: Element) {
  const calls: string[] = [];
  const dispatcher = createDispatcher({
    registry: buildRegistry(),
    getHandler: (name) => () => calls.push(name),
  });

  const event = new KeyboardEvent("keydown", {
    key: "Escape",
    cancelable: true,
    bubbles: true,
  });
  Object.defineProperty(event, "target", { value: target });

  dispatcher.handleKeyDown(event);
  return calls;
}

afterEach(() => {
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("Escape with a modal open", () => {
  it("closes the modal and does not pause the chat behind it", () => {
    const input = openModal();

    const calls = pressEscape(input);

    expect(calls).toEqual(["onDismissModal"]);
    expect(calls).not.toContain("onStopStreaming");
  });

  it("stops the response again once the modal closes", () => {
    // The modal context must be transient: after the overlay unmounts, Escape
    // goes back to being the "stop the AI" key.
    openModal();
    document.body.innerHTML = "";

    expect(pressEscape(document.body)).toEqual(["onStopStreaming"]);
  });

  it("is not fooled by a closed modal left in the tree", () => {
    // Overlays render `data-modal-open` only while open, so a node without the
    // attribute must not put us in the modal context.
    const overlay = document.createElement("div");
    overlay.setAttribute("data-modal-open", "false");
    document.body.appendChild(overlay);

    expect(pressEscape(document.body)).toEqual(["onStopStreaming"]);
  });
});
