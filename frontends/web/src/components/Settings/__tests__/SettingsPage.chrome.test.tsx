import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({
  navigate: vi.fn(),
  section: "account",
  pathname: "/settings/account",
  isElectron: false,
  isMac: false,
  isFullscreen: false,
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => state.navigate,
  useParams: () => ({ section: state.section }),
  useSearch: () => ({}),
  useLocation: () => ({ pathname: state.pathname }),
}));
vi.mock("../../../services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("../../Orgs/OrgSwitcher", () => ({
  OrgSwitcher: () => <button aria-label="Switch organization">Organization</button>,
}));
vi.mock("../../../hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({
    ...state,
    dragRegionStyle: state.isElectron ? { WebkitAppRegion: "drag" } : {},
    noDragRegionStyle: state.isElectron ? { WebkitAppRegion: "no-drag" } : {},
  }),
}));
vi.mock("../SettingsContent", () => ({
  SettingsContent: () => (
    <div data-testid="settings-content">
      <h1>Account</h1>
      <input aria-label="Name" />
      <select aria-label="Preference"><option>One</option></select>
      <button>Content action</button>
    </div>
  ),
}));

import { SettingsPage } from "../SettingsPage";

let resize: ResizeObserverCallback;
beforeEach(() => {
  state.navigate.mockReset();
  state.section = "account";
  state.pathname = "/settings/account";
  state.isElectron = false;
  state.isMac = false;
  state.isFullscreen = false;
  vi.stubGlobal("ResizeObserver", class {
    constructor(callback: ResizeObserverCallback) { resize = callback; }
    observe() {}
    disconnect() {}
  });
});

function setWidth(width: number) {
  act(() => resize([{ contentRect: { width } } as ResizeObserverEntry], {} as ResizeObserver));
}

describe("settings navigation chrome", () => {
  it("has one unboxed sidebar exit and only the section's page heading", async () => {
    const { container } = render(<SettingsPage />);
    await screen.findByTestId("settings-content");
    const sidebar = screen.getByRole("complementary", { name: "Settings" });
    const exit = within(sidebar).getByRole("button", { name: "Back to app" });
    expect(container.querySelectorAll("header")).toHaveLength(1);
    expect(sidebar.querySelector("header")?.className).not.toMatch(/border|bg-/);
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.queryByText("Configure Reliant")).not.toBeInTheDocument();
    fireEvent.click(exit);
    expect(state.navigate).toHaveBeenCalledWith({ to: "/", search: {} });
  });

  it("keeps a labelled, keyboard-accessible exit in the narrow rail without a 500px minimum", async () => {
    const { container } = render(<SettingsPage />);
    await screen.findByTestId("settings-content");
    setWidth(375);
    const exit = screen.getByRole("button", { name: "Back to app" });
    act(() => exit.focus());
    expect(exit).toHaveFocus();
    expect(exit.textContent).toBe("");
    expect(container.innerHTML).not.toContain("min-w-[500px]");
    fireEvent.click(exit);
    expect(state.navigate).toHaveBeenCalledWith({ to: "/", search: {} });
    expect(screen.getByRole("button", { name: "AI providers" })).toBeInTheDocument();
    const switcher = screen.getByRole("button", { name: "Switch organization" });
    expect(switcher).toBeInTheDocument();
    expect(switcher.parentElement?.className).toContain("w-full");
  });

  it("moves section focus and routing together; does not steal arrows from forms or the exit", async () => {
    render(<SettingsPage />);
    await screen.findByTestId("settings-content");
    const account = screen.getByRole("button", { name: "Account" });
    account.focus();
    fireEvent.keyDown(account, { key: "ArrowDown" });
    expect(state.navigate).toHaveBeenCalledWith({ to: "/settings/$section", params: { section: "billing" } });
    expect(screen.getByRole("button", { name: "Billing & usage" })).toHaveFocus();
    state.navigate.mockClear();
    for (const target of [screen.getByRole("combobox"), screen.getByRole("textbox"), screen.getByRole("button", { name: "Content action" }), screen.getByRole("button", { name: "Back to app" })]) {
      fireEvent.keyDown(target, { key: "ArrowDown" });
    }
    expect(state.navigate).not.toHaveBeenCalled();
  });

  it("uses the URL section on rerender (including browser history changes)", async () => {
    const view = render(<SettingsPage />);
    await screen.findByTestId("settings-content");
    expect(screen.getByRole("button", { name: "Account" })).toHaveAttribute("aria-current", "page");
    state.section = "appearance";
    view.rerender(<SettingsPage />);
    expect(screen.getByRole("button", { name: "Appearance" })).toHaveAttribute("aria-current", "page");
    state.section = "account";
    view.rerender(<SettingsPage />);
    expect(screen.getByRole("button", { name: "Account" })).toHaveAttribute("aria-current", "page");
  });

  it("Escape exits safely, but leaves form and dialog Escape to their owners", async () => {
    render(<SettingsPage />);
    await screen.findByTestId("settings-content");
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Escape" });
    fireEvent.keyDown(screen.getByRole("combobox"), { key: "Escape" });
    const dialog = document.createElement("div");
    dialog.setAttribute("aria-modal", "true");
    document.body.append(dialog);
    fireEvent.keyDown(document.body, { key: "Escape" });
    dialog.remove();
    expect(state.navigate).not.toHaveBeenCalled();
    fireEvent.keyDown(screen.getByRole("button", { name: "Back to app" }), { key: "Escape" });
    expect(state.navigate).toHaveBeenCalledWith({ to: "/", search: {} });
  });

  it("keeps Electron drag regions, excludes the exit from dragging and clears macOS traffic lights", async () => {
    state.isElectron = true;
    state.isMac = true;
    const view = render(<SettingsPage />);
    await screen.findByTestId("settings-content");
    setWidth(375);
    const sidebar = screen.getByRole("complementary", { name: "Settings" });
    const header = sidebar.querySelector("header")!;
    // jsdom keeps unsupported Electron CSS as a style property, not an attribute.
    expect((header.style as unknown as { WebkitAppRegion: string }).WebkitAppRegion).toBe("drag");
    expect((screen.getByRole("button", { name: "Close Settings" }).style as unknown as { WebkitAppRegion: string }).WebkitAppRegion).toBe("no-drag");
    expect(sidebar.className).toContain("w-24");
    expect(sidebar.firstElementChild).toHaveAttribute("aria-hidden", "true");
    expect(sidebar.firstElementChild?.className).toContain("h-10");
    state.isFullscreen = true;
    view.rerender(<SettingsPage />);
    expect(sidebar.className).toContain("w-16");
    expect(sidebar.firstElementChild).toBe(header);
  });
});
