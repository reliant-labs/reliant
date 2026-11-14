// Copyright (c) 2025 Reliant Labs
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { ForgeLayout } from "../../ForgeLayout";
import { DemoTips } from "../tips/DemoTips";
import { hasOpenEscapeLayer } from "@/hooks/useEscapeLayer";

function mount(width: number) {
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("1023px") && width <= 1023, media: query, onchange: null, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }));
  const client = new QueryClient({defaultOptions:{queries:{retry:false}}});
  const root = createRootRoute({ component: () => <QueryClientProvider client={client}><Outlet /><DemoTips /></QueryClientProvider> });
  const layout = createRoute({ getParentRoute: () => root, id: "forge", component: ForgeLayout });
  const page = createRoute({ getParentRoute: () => layout, path: "$", validateSearch: (search: Record<string, unknown>) => search, component: () => <main><button>Page action</button></main> });
  const router = createRouter({ routeTree: root.addChildren([layout.addChildren([page])]), history: createMemoryHistory({ initialEntries: ["/forge/p/shop?env=prod&range=6h"] }) });
  render(<RouterProvider router={router} />);
  return router;
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it.each([360, 390, 768, 1023, 1440])("keeps Escape local after Done/About reopening with ForgeLayout capture at %ipx", async (width) => {
  const router = mount(width);
  await screen.findByTestId("tour-callout");
  const original = router.state.location.href;
  fireEvent.click(screen.getByTestId("tour-next"));
  fireEvent.click(await screen.findByTestId("demo-page-info"));
  expect(await screen.findByTestId("tour-callout")).toBeVisible();
  expect(hasOpenEscapeLayer()).toBe(true);
  const escape = new KeyboardEvent("keydown", {key:"Escape", bubbles:true, cancelable:true});
  fireEvent(screen.getByTestId("tour-next"), escape);
  await waitFor(() => expect(screen.queryByTestId("tour-callout")).toBeNull());
  expect(router.state.location.href).toBe(original);
  expect(screen.getByTestId("demo-start-tour")).toHaveFocus();
  expect(hasOpenEscapeLayer()).toBe(false);
});

it.each([390, 1440])("closes contextual How before the underlying page explanation at %ipx", async (width) => {
  const router = mount(width);
  const card = await screen.findByTestId("tour-callout");
  const original = router.state.location.href;
  const trigger = within(card).getByRole("button", {name:"How Forge builds this"});
  trigger.focus(); fireEvent.click(trigger);
  const dialog = await screen.findByRole("dialog", {name:"How Forge builds this"});
  await within(dialog).findByTestId("how-built");
  fireEvent.keyDown(within(dialog).getByRole("button", {name:"Back to exploring"}), {key:"Escape"});
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(screen.getByTestId("tour-callout")).toBeVisible();
  expect(trigger).toHaveFocus();
  expect(router.state.location.href).toBe(original);
  fireEvent.keyDown(trigger, {key:"Escape"});
  await waitFor(() => expect(screen.queryByTestId("tour-callout")).toBeNull());
  expect(router.state.location.href).toBe(original);
  expect(screen.getByTestId("demo-start-tour")).toHaveFocus();
});
