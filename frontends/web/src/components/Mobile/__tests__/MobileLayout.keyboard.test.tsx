import { describe, it, expect, vi, afterEach } from "vitest";
import { render, act } from "@testing-library/react";
import { MobileLayout } from "../MobileLayout";
import { MobileEmptyState } from "../MobileChrome";
import { MessageSquare } from "lucide-react";

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
    set(next: { height: number; offsetTop?: number; scale?: number }) {
      vv.height = next.height;
      vv.offsetTop = next.offsetTop ?? 0;
      vv.scale = next.scale ?? 1;
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
  it("gives short-height empty content its own scroll region and safe centering", () => {
    const { getByRole } = render(
      <MobileLayout>
        <MobileEmptyState icon={MessageSquare} title="No chats" action={<button>Start chat</button>} />
      </MobileLayout>,
    );
    const content = getByRole("button", { name: "Start chat" }).parentElement!.parentElement!;
    expect(content.className).toContain("my-auto");
    expect(content.className).toContain("shrink-0");
    expect(content.parentElement!.className).toContain("min-h-0");
    expect(content.parentElement!.className).toContain("overflow-y-auto");
  });

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

  it("preserves the mounted draft across keyboard dismissal, zoom and rotation", () => {
    const vv = installVisualViewport(844);
    const { container } = render(<MobileLayout><input defaultValue="unsent draft" /></MobileLayout>);
    const input = container.querySelector("input");
    vv.set({ height: 508, offsetTop: 120 });
    vv.set({ height: 844 });
    expect(shell(container).style.height).toBe("");
    expect(shell(container).style.top).toBe("");
    expect(shell(container).style.position).toBe("");
    expect(shell(container).style.getPropertyValue("--mobile-safe-bottom")).not.toBe("0px");
    vv.set({ height: 422, scale: 2 });
    expect(shell(container).style.position).toBe("");
    vv.set({ height: 390 });
    act(() => {
      Object.defineProperty(window, "innerHeight", { value: 390, configurable: true });
      window.dispatchEvent(new Event("resize"));
    });
    expect(shell(container).style.position).toBe("");
    expect(container.querySelector("input")).toBe(input);
    expect(input?.value).toBe("unsent draft");
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
