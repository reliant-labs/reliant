import { createRootRoute } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";

import { ShellFrameContext, useNavDrawer } from "./components/Layout/shell/shellFrame";
import { createDemoAgentsRoutes } from "./demoAgentsRoutes";

vi.mock("./lib/lazyRoute", () => ({ lazyRoute: () => () => null }));

afterEach(() => {
  cleanup();
  useNavDrawer.getState().setOpen(false);
  vi.restoreAllMocks();
});

it.each(["/settings/machines", "/settings/machines/$machineId"])("can reopen the phone drawer from %s", (path) => {
  vi.spyOn(window, "matchMedia").mockImplementation((query) => ({
    matches: true, media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(),
    addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(() => false),
  }));
  const root = createRootRoute();
  const route = createDemoAgentsRoutes(() => root).find((candidate) => candidate.options.path === path);
  expect(route).toBeDefined();
  const Page = route!.options.component as () => React.ReactNode;
  render(<ShellFrameContext.Provider value={{ inShell: true, navDocked: false }}><Page /></ShellFrameContext.Provider>);
  expect(useNavDrawer.getState().open).toBe(false);
  fireEvent.click(screen.getByRole("button", { name: "Show sidebar" }));
  expect(useNavDrawer.getState().open).toBe(true);
});
