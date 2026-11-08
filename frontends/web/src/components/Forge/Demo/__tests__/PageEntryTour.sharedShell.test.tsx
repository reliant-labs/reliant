// Copyright (c) 2025 Reliant Labs
import { afterEach, beforeEach, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { resetShellForTests } from "@/lib/shell";
import { PageEntryTour } from "../tips/PageEntryTour";

beforeEach(() => resetShellForTests("reliant"));
afterEach(() => { cleanup(); resetShellForTests(); });

it.each(["Done", "Escape"] as const)("dismisses with %s in the Reliant shell without demo chrome or a focus target", async (method) => {
  // Mount the shared explanation alone, not DemoTips: a signed-in shell has
  // no DemoScreenActions and therefore no demo-start-tour focus destination.
  const root = createRootRoute({ component: () => <><Outlet /><PageEntryTour /></> });
  const route = createRoute({
    getParentRoute: () => root,
    path: "$",
    validateSearch: (search: Record<string, unknown>) => search,
    component: () => <main><button>Page action</button></main>,
  });
  const router = createRouter({
    routeTree: root.addChildren([route]),
    history: createMemoryHistory({ initialEntries: ["/forge/p/shop/observability/logs?range=6h&env=prod"] }),
  });
  render(<RouterProvider router={router} />);
  await screen.findByTestId("tour-callout");
  expect(document.querySelector('[data-testid="demo-start-tour"]')).toBeNull();
  expect(screen.queryByTestId("demo-screen-actions")).toBeNull();
  const before = router.state.location.href;
  const errors: ErrorEvent[] = [];
  const onError = (event: ErrorEvent) => { errors.push(event); };
  window.addEventListener("error", onError);
  try {
    if (method === "Done") {
      expect(() => fireEvent.click(screen.getByTestId("tour-next"))).not.toThrow();
    } else {
      const pageAction = screen.getByRole("button", { name: "Page action" });
      pageAction.focus();
      expect(() => fireEvent.keyDown(pageAction, { key: "Escape" })).not.toThrow();
      expect(pageAction).toHaveFocus();
    }
    await waitFor(() => expect(screen.queryByTestId("tour-callout")).toBeNull());
    expect(errors).toEqual([]);
    expect(router.state.location.href).toBe(before);
    expect(screen.getByTestId("demo-page-info")).toBeVisible();
    fireEvent.click(screen.getByTestId("demo-page-info"));
    expect(await screen.findByTestId("tour-callout")).toBeVisible();
    expect(router.state.location.href).toBe(before);
  } finally {
    window.removeEventListener("error", onError);
  }
});
