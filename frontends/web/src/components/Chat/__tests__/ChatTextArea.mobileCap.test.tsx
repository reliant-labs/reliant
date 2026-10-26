import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, act, fireEvent } from "@testing-library/react";
import { useState } from "react";
import { ChatTextArea } from "../ChatTextArea";
import { SurfaceProvider } from "../../../lib/surfaceContext";

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
}));

vi.mock("../../../store/chatNavigationStore", () => ({
  useChatNavigationStore: { getState: () => ({ navigateToChat: vi.fn() }) },
}));

vi.mock("../../../lib/fileOpener", () => ({ useFileOpener: () => vi.fn() }));

const LINE = 24;
const editor = () => screen.getByTestId("chat-input") as HTMLTextAreaElement;

function Harness({ initial }: { initial: string }) {
  const [value, setValue] = useState(initial);
  return <ChatTextArea value={value} onChange={setValue} onSend={() => {}} />;
}

/** A visualViewport stand-in that tests can resize like a keyboard opening. */
function installVisualViewport(height: number) {
  const listeners = new Set<() => void>();
  const vv = {
    height,
    offsetTop: 0,
    scale: 1,
    addEventListener: (_: string, fn: () => void) => listeners.add(fn),
    removeEventListener: (_: string, fn: () => void) => listeners.delete(fn),
  };
  Object.defineProperty(window, "visualViewport", { value: vv, configurable: true });
  return {
    resize(next: number) {
      vv.height = next;
      act(() => listeners.forEach((fn) => fn()));
    },
  };
}

beforeEach(() => {
  // jsdom lays nothing out: report a content height from the line count and a
  // fixed line height, which is all the autosize logic reads.
  vi.spyOn(window, "getComputedStyle").mockReturnValue({
    lineHeight: `${LINE}px`,
    paddingTop: "0px",
    paddingBottom: "0px",
  } as CSSStyleDeclaration);
  Object.defineProperty(HTMLTextAreaElement.prototype, "scrollHeight", {
    configurable: true,
    get(this: HTMLTextAreaElement) {
      return Math.max(1, this.value.split("\n").length) * LINE;
    },
  });
  Object.defineProperty(window, "innerHeight", { value: 844, configurable: true });
});

afterEach(() => {
  vi.restoreAllMocks();
  Reflect.deleteProperty(window, "visualViewport");
});

const THIRTY_LINES = Array.from({ length: 30 }, (_, i) => `line ${i}`).join("\n");

describe("composer auto-grow cap", () => {
  it("on mobile, follows the visible viewport and shrinks when the keyboard opens", () => {
    const vv = installVisualViewport(844);
    render(
      <SurfaceProvider surface="mobile">
        <Harness initial={THIRTY_LINES} />
      </SurfaceProvider>,
    );
    // 20% of 844 = 168.
    expect(editor().style.height).toBe("168px");

    vv.resize(844 - 336);
    // 20% of 508 = 101: the timeline keeps the rest.
    expect(editor().style.height).toBe("101px");
    expect(editor().style.maxHeight).toBe("101px");
  });

  it("still grows with the text below the cap", () => {
    installVisualViewport(500);
    render(
      <SurfaceProvider surface="mobile">
        <Harness initial={"a\nb\nc"} />
      </SurfaceProvider>,
    );
    expect(editor().style.height).toBe("72px");
  });

  it("on desktop, ignores the viewport: ten lines", () => {
    installVisualViewport(300);
    render(<Harness initial={THIRTY_LINES} />);
    expect(editor().style.height).toBe("200px");
  });

  it("keeps the caret in view when it is at the end of a capped composer", () => {
    installVisualViewport(500);
    render(
      <SurfaceProvider surface="mobile">
        <Harness initial={THIRTY_LINES} />
      </SurfaceProvider>,
    );
    const el = editor();
    el.focus();
    el.setSelectionRange(el.value.length, el.value.length);
    // jsdom's scrollTop is a no-op; give the element a real one.
    let top = 0;
    Object.defineProperty(el, "scrollTop", { configurable: true, get: () => top, set: (v) => (top = v) });
    // Typing one more character re-runs the autosize.
    fireEvent.change(el, { target: { value: `${THIRTY_LINES}!` } });
    expect(el.scrollTop).toBe(el.scrollHeight);
  });
});
