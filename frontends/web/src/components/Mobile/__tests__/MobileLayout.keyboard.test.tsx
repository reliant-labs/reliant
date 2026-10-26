import { describe, it, expect, vi, afterEach } from "vitest";
import { render, act } from "@testing-library/react";
import { MobileLayout } from "../MobileLayout";

vi.mock("../../../services/settingsSync", () => ({
  settingsSync: { getSetting: (_: string, fallback: unknown) => fallback },
  SETTINGS_KEYS: { FONT_SIZE: "font-size" },
}));

vi.mock("../../../lib/rootFontSize", () => ({
  applyRootFontSize: vi.fn(),
  DEFAULT_FONT_SIZE: "medium",
}));

function installVisualViewport(height: number, offsetTop = 0) {
  const listeners = new Set<() => void>();
  const vv = {
    height,
    offsetTop,
    scale: 1,
    addEventListener: (_: string, fn: () => void) => listeners.add(fn),
    removeEventListener: (_: string, fn: () => void) => listeners.delete(fn),
  };
  Object.defineProperty(window, "visualViewport", { value: vv, configurable: true });
  Object.defineProperty(window, "innerHeight", { value: 844, configurable: true });
  return {
    set(next: { height: number; offsetTop?: number }) {
      vv.height = next.height;
      vv.offsetTop = next.offsetTop ?? 0;
      act(() => listeners.forEach((fn) => fn()));
    },
  };
}

afterEach(() => {
  Reflect.deleteProperty(window, "visualViewport");
});

const shell = (container: HTMLElement) =>
  container.querySelector<HTMLElement>('[data-surface="mobile"]')!;

describe("MobileLayout", () => {
  it("fills the dynamic viewport while the keyboard is closed", () => {
    installVisualViewport(844);
    const { container } = render(
      <MobileLayout>
        <div />
      </MobileLayout>,
    );
    expect(shell(container).className).toContain("h-[100dvh]");
    expect(shell(container).style.position).toBe("");
    // jsdom's style parser rejects env(), so only the keyboard-open branch
    // (a plain 0px) is observable here; the closed value is checked in-browser.
  });

  it("shrinks to the visible viewport and drops the home-indicator reserve when the keyboard opens", () => {
    const vv = installVisualViewport(844);
    const { container } = render(
      <MobileLayout>
        <div />
      </MobileLayout>,
    );
    vv.set({ height: 508, offsetTop: 0 });
    const el = shell(container);
    expect(el.style.position).toBe("fixed");
    expect(el.style.height).toBe("508px");
    expect(el.style.top).toBe("0px");
    expect(el.style.getPropertyValue("--mobile-safe-bottom")).toBe("0px");
  });

  it("follows the pan offset iOS applies while the keyboard is open", () => {
    const vv = installVisualViewport(844);
    const { container } = render(
      <MobileLayout>
        <div />
      </MobileLayout>,
    );
    vv.set({ height: 508, offsetTop: 120 });
    expect(shell(container).style.top).toBe("120px");
  });

  it("does not treat a collapsing URL bar as a keyboard", () => {
    const vv = installVisualViewport(844);
    const { container } = render(
      <MobileLayout>
        <div />
      </MobileLayout>,
    );
    vv.set({ height: 844 - 80 });
    expect(shell(container).style.position).toBe("");
  });
});
