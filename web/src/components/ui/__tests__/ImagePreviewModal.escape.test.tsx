/**
 * Escape must close the full-screen image preview.
 *
 * The bug this guards was NOT a missing listener — ImagePreviewModal had two
 * (a capture-phase document listener and an onKeyDown on the focused
 * container) and neither fired. The global shortcut dispatcher owns Escape:
 * it registers ONE capture-phase listener at `document`, and because the
 * preview renders `data-modal-open="true"`, detectActiveContexts puts the
 * dispatcher in the `modal` context, where Escape resolves to the
 * `dismissModal` shortcut. That shortcut is not `passthrough`, so the
 * dispatcher calls stopImmediatePropagation().
 *
 * The dispatcher's listener is registered when ModernApp mounts, long before
 * any preview opens, so at `document` capture it runs FIRST. Its
 * stopImmediatePropagation() then kills every later capture listener on the
 * same node (the modal's own) and prevents the event from ever reaching the
 * bubble phase (React's onKeyDown). Both of the modal's paths are dead by
 * construction, and the dispatcher's `onDismissModal` handler only closes the
 * modals whose open state ModernApp itself holds — not this one.
 *
 * So the fix is to let the preview be dismissed BY that contract instead of
 * racing it. These tests drive the real dispatcher, wired exactly as
 * useAppKeyboardShortcuts wires it, against a real ImagePreviewModal.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { render, act, screen } from "@testing-library/react";
import { ImagePreviewModal } from "../ImagePreviewModal";
import { defaultShortcuts } from "../../../store/shortcutsData.generated";
import { parseBinding } from "../../../lib/keyboard/chord";
import {
  ShortcutRegistry,
  type ResolvedShortcut,
} from "../../../lib/keyboard/registry";
import { createDispatcher } from "../../../lib/keyboard/dispatcher";

vi.mock("../../../hooks/useFocusManager", () => ({
  focusChatInput: vi.fn(),
}));

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

/**
 * Mount the global dispatcher the way useAppKeyboardShortcuts does: one
 * capture-phase listener at the document root, registered BEFORE any overlay
 * opens. Returns the handler names it fired.
 */
const teardown: Array<() => void> = [];

function mountGlobalDispatcher(): { fired: string[] } {
  const fired: string[] = [];
  const dispatcher = createDispatcher({
    registry: buildRegistry(),
    getHandler: (name) => () => fired.push(name),
  });
  const onKeyDown = (event: KeyboardEvent) => dispatcher.handleKeyDown(event);
  document.addEventListener("keydown", onKeyDown, true);
  // Registered for teardown immediately, not at the end of the test body: a
  // failing assertion throws, and a listener leaked onto the shared `document`
  // would silently swallow Escape in every test that ran afterwards.
  teardown.push(() =>
    document.removeEventListener("keydown", onKeyDown, true),
  );
  return { fired };
}

function pressEscape() {
  act(() => {
    // Dispatch from whatever the modal focused, exactly as a real keypress
    // would, so the event travels the real capture path through `document`.
    const target = document.activeElement ?? document.body;
    target.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Escape",
        bubbles: true,
        cancelable: true,
      }),
    );
  });
}

afterEach(() => {
  while (teardown.length) teardown.pop()!();
  vi.restoreAllMocks();
});

describe("why the modal's own Escape listeners never fired", () => {
  // This is the diagnosis, pinned as a test so the claim above is not just a
  // comment. It asserts the MECHANISM, independent of ImagePreviewModal, so it
  // keeps documenting the constraint even if the modal is rewritten.
  it("the dispatcher's stopImmediatePropagation kills later document-capture listeners", () => {
    const dispatcher = mountGlobalDispatcher();

    // A modal that opens AFTER the dispatcher — the real mount order, since the
    // dispatcher is registered when ModernApp mounts.
    const overlay = document.createElement("div");
    overlay.setAttribute("data-modal-open", "true");
    document.body.appendChild(overlay);
    teardown.push(() => overlay.remove());

    const modalCaptureListener = vi.fn();
    const modalBubbleListener = vi.fn();
    document.addEventListener("keydown", modalCaptureListener, true);
    document.addEventListener("keydown", modalBubbleListener, false);
    teardown.push(() => {
      document.removeEventListener("keydown", modalCaptureListener, true);
      document.removeEventListener("keydown", modalBubbleListener, false);
    });

    pressEscape();

    // The dispatcher claimed Escape via the `modal` context...
    expect(dispatcher.fired).toEqual(["onDismissModal"]);
    // ...and neither of the modal's two paths ever ran. Capture is dead because
    // stopImmediatePropagation() halts remaining listeners on the same node;
    // bubble (React's onKeyDown) is dead because propagation stopped entirely.
    expect(modalCaptureListener).not.toHaveBeenCalled();
    expect(modalBubbleListener).not.toHaveBeenCalled();
  });
});

describe("ImagePreviewModal Escape handling", () => {
  it("closes when Escape is pressed while the global dispatcher owns Escape", () => {
    const dispatcher = mountGlobalDispatcher();
    const onClose = vi.fn();

    render(
      <ImagePreviewModal
        isOpen
        onClose={onClose}
        imageUrl="blob:generated-image"
        filename="generated.png"
      />,
    );

    pressEscape();

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("does not let Escape pause the chat streaming behind the preview", () => {
    const dispatcher = mountGlobalDispatcher();
    const onClose = vi.fn();

    render(
      <ImagePreviewModal
        isOpen
        onClose={onClose}
        imageUrl="blob:generated-image"
        filename="generated.png"
      />,
    );

    pressEscape();

    expect(dispatcher.fired).not.toContain("onStopStreaming");
  });

  it("still closes on Escape when no global dispatcher is mounted", () => {
    // The preview is also used from routes where ModernApp (and therefore the
    // dispatcher) is not mounted, so it must not depend on it existing.
    const onClose = vi.fn();

    render(
      <ImagePreviewModal
        isOpen
        onClose={onClose}
        imageUrl="blob:generated-image"
        filename="generated.png"
      />,
    );

    pressEscape();

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("stops claiming Escape once it is closed", () => {
    const dispatcher = mountGlobalDispatcher();
    const onClose = vi.fn();

    const { rerender } = render(
      <ImagePreviewModal
        isOpen
        onClose={onClose}
        imageUrl="blob:generated-image"
        filename="generated.png"
      />,
    );

    rerender(
      <ImagePreviewModal
        isOpen={false}
        onClose={onClose}
        imageUrl="blob:generated-image"
        filename="generated.png"
      />,
    );

    pressEscape();

    expect(onClose).not.toHaveBeenCalled();
    // With the overlay gone, Escape goes back to being the "stop the AI" key.
    expect(dispatcher.fired).toEqual(["onStopStreaming"]);
  });

  it("closes on the close button too", () => {
    const onClose = vi.fn();

    render(
      <ImagePreviewModal
        isOpen
        onClose={onClose}
        imageUrl="blob:generated-image"
        filename="generated.png"
      />,
    );

    act(() => {
      screen.getByTitle("Close (ESC)").click();
    });

    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
