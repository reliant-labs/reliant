// Copyright (c) 2025 Reliant Labs
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { ForgeLayout } from "../../ForgeLayout";
import { DemoTips } from "../tips/DemoTips";
import { hasOpenEscapeLayer } from "@/hooks/useEscapeLayer";
import { resetDemoSessionForTests } from "../demoSession";

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
beforeEach(() => resetDemoSessionForTests());
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

it.each([1024, 1440])("keeps Escape local after Done/? reopening with ForgeLayout capture at %ipx", async (width) => {
  const router = mount(width);
  await screen.findByTestId("tour-callout");
  const original = router.state.location.href;
  fireEvent.click(screen.getByTestId("tour-next"));
  const about = await screen.findByTestId("demo-page-info");
  about.focus();
  fireEvent.click(about);
  expect(await screen.findByTestId("tour-callout")).toBeVisible();
  expect(hasOpenEscapeLayer()).toBe(true);
  const escape = new KeyboardEvent("keydown", {key:"Escape", bubbles:true, cancelable:true});
  screen.getByTestId("tour-next").focus();
  fireEvent(screen.getByTestId("tour-next"), escape);
  await waitFor(() => expect(screen.queryByTestId("tour-callout")).toBeNull());
  expect(router.state.location.href).toBe(original);
  // Focus was in the card: it returns to "?", never to an unrelated control.
  expect(screen.getByTestId("demo-page-info")).toHaveFocus();
  expect(hasOpenEscapeLayer()).toBe(false);
});

it.each([360, 390, 768, 1023])("on a phone at %ipx, the tip is a closed one-line disclosure in the page", async (width) => {
  const router = mount(width);
  const about = await screen.findByTestId("demo-page-info");
  expect(about).toHaveTextContent("About this page");
  expect(about).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByTestId("tour-callout")).toBeNull();
  fireEvent.click(about);
  expect(await screen.findByTestId("tour-callout")).toBeVisible();
  // In the page's flow: no layer of its own over the page.
  expect(hasOpenEscapeLayer()).toBe(false);
  fireEvent.click(about);
  await waitFor(() => expect(screen.queryByTestId("tour-callout")).toBeNull());
  expect(router.state.location.pathname).toBe("/forge/p/shop");
});
