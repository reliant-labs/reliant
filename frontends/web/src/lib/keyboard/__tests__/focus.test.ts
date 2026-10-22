import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearPane,
  focusEventName,
  focusPane,
  focusPrevious,
  getCurrentPane,
  isFocusWithin,
  PANES,
  type Pane,
} from "../focus";

/** Record which pane-focus events fire, in order. */
function recordEvents(): { fired: string[]; cleanup: () => void } {
  const fired: string[] = [];
  const offs = PANES.map((pane) => {
    const name = focusEventName(pane);
    const listener = () => fired.push(pane);
    window.addEventListener(name, listener);
    return () => window.removeEventListener(name, listener);
  });
  return { fired, cleanup: () => offs.forEach((off) => off()) };
}

describe("focusPane", () => {
  beforeEach(() => {
    // Reset tracking between tests — the module holds state deliberately.
    focusPane("chat-input");
    clearPane("chat-input");
  });

  it("dispatches the event for the requested pane", () => {
    const { fired, cleanup } = recordEvents();

    focusPane("terminal");

    expect(fired).toEqual(["terminal"]);
    cleanup();
  });

  it("tracks the pane focus moved to", () => {
    focusPane("editor");
    expect(getCurrentPane()).toBe("editor");
  });

  it("re-dispatches when asked for the pane that already has focus", () => {
    // Pressing "focus the terminal" twice should still focus it — the user may
    // have clicked elsewhere without us noticing.
    const { fired, cleanup } = recordEvents();

    focusPane("terminal");
    focusPane("terminal");

    expect(fired).toEqual(["terminal", "terminal"]);
    cleanup();
  });
});

describe("focusPrevious", () => {
  it("returns to the pane focus came from", () => {
    const { fired, cleanup } = recordEvents();

    focusPane("chat-input");
    focusPane("right-sidebar");
    fired.length = 0;

    focusPrevious();

    expect(fired).toEqual(["chat-input"]);
    expect(getCurrentPane()).toBe("chat-input");
    cleanup();
  });

  it("falls back to the composer when there is nowhere to go back to", () => {
    // The composer is always present and always accepts typing, so landing
    // there is never a dead end.
    const { fired, cleanup } = recordEvents();

    focusPane("terminal");
    clearPane("terminal");
    fired.length = 0;

    focusPrevious();

    expect(fired).toEqual(["chat-input"]);
    cleanup();
  });

  it("honours an explicit fallback", () => {
    const { fired, cleanup } = recordEvents();

    // Clear both the current and previous pane so there is genuinely nowhere
    // to return to — clearPane only steps back one level.
    focusPane("editor");
    clearPane("editor");
    clearPane("chat-input");
    fired.length = 0;

    focusPrevious("transcript");

    expect(fired).toEqual(["transcript"]);
    cleanup();
  });

  it("does not bounce back and forth on repeated calls", () => {
    // Going back twice should not return to the pane we just left.
    focusPane("chat-input");
    focusPane("terminal");

    focusPrevious();
    expect(getCurrentPane()).toBe("chat-input");

    focusPrevious();
    expect(getCurrentPane()).toBe("chat-input");
  });
});

describe("clearPane", () => {
  it("stops focusPrevious returning into a pane that was closed", () => {
    const { fired, cleanup } = recordEvents();

    focusPane("right-sidebar");
    focusPane("editor");
    // The sidebar gets closed while the editor has focus.
    clearPane("right-sidebar");
    fired.length = 0;

    focusPrevious();

    // Must not send the user back into a hidden pane.
    expect(fired).not.toEqual(["right-sidebar"]);
    cleanup();
  });

  it("steps back when the current pane is the one cleared", () => {
    focusPane("chat-input");
    focusPane("right-sidebar");

    clearPane("right-sidebar");

    expect(getCurrentPane()).toBe("chat-input");
  });
});

describe("isFocusWithin", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  const mount = (pane: Pane | "none", selectorAttr: string) => {
    document.body.innerHTML = `<div ${selectorAttr}><button id="inner">x</button></div>`;
    document.getElementById("inner")?.focus();
    return pane;
  };

  it("detects focus inside a pane by its context marker", () => {
    mount("right-sidebar", 'data-context="right-sidebar"');
    expect(isFocusWithin("right-sidebar")).toBe(true);
    expect(isFocusWithin("chat-input")).toBe(false);
  });

  it("detects the terminal by its xterm container", () => {
    mount("terminal", 'class="xterm"');
    expect(isFocusWithin("terminal")).toBe(true);
  });

  it("detects the editor by its monaco container", () => {
    mount("editor", 'class="monaco-editor"');
    expect(isFocusWithin("editor")).toBe(true);
  });

  it("returns false when focus is outside every pane", () => {
    document.body.innerHTML = `<button id="loose">x</button>`;
    document.getElementById("loose")?.focus();

    for (const pane of PANES) {
      expect(isFocusWithin(pane)).toBe(false);
    }
  });
});

describe("event naming", () => {
  it("namespaces pane events so they cannot collide with other custom events", () => {
    expect(focusEventName("chat-input")).toBe("focus-pane:chat-input");
  });

  it("gives every pane a distinct event", () => {
    const names = new Set(PANES.map(focusEventName));
    expect(names.size).toBe(PANES.length);
  });
});

describe("integration with the shortcut layer", () => {
  it("covers every pane a focus shortcut can target", async () => {
    // Guards against adding a focus shortcut without a pane to focus.
    const { defaultShortcuts } = await import(
      "../../../store/shortcutsData.generated"
    );
    const focusHandlers = Object.values(defaultShortcuts)
      .filter((s) => s.category === "Focus")
      .map((s) => s.handler);

    expect(focusHandlers.length).toBeGreaterThan(0);
    // Every Focus-category shortcut should name a pane we can actually reach,
    // except the "go back" one which targets whichever pane you came from.
    const expected = new Set([
      "onFocusChat",
      "onFocusTranscript",
      "onFocusTerminal",
      "onFocusLeftSidebar",
      "onFocusRightSidebar",
      "onFocusFileEditor",
      "onFocusPrevious",
    ]);
    for (const handler of focusHandlers) {
      expect(expected.has(handler)).toBe(true);
    }
  });
});

describe("mocked dispatch", () => {
  it("uses CustomEvent so listeners can be added and removed cleanly", () => {
    const spy = vi.fn();
    const name = focusEventName("transcript");
    window.addEventListener(name, spy);

    focusPane("transcript");
    expect(spy).toHaveBeenCalledOnce();

    window.removeEventListener(name, spy);
    focusPane("transcript");
    expect(spy).toHaveBeenCalledOnce();
  });
});
