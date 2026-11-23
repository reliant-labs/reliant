// Copyright (c) 2026 Reliant Labs

/**
 * `/forge/demo` in the app's tree: signed in, the page an old demo link names
 * on the sample app in the shell; signed out, the demo panel.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../Demo/demoRoute", () => ({ ForgeDemoPage: () => <div data-testid="demo-panel" /> }));

import { useAuthStore } from "@/store/authStore";

import { ForgeDemoEntry, sampleHrefForDemo } from "../ForgeDemoEntry";

describe("sampleHrefForDemo", () => {
  it("opens the sample app's page for the panel's view, keeping the open trace and the tour", () => {
    expect(sampleHrefForDemo({})).toBe("/forge/p/sample_shop");
    expect(sampleHrefForDemo({ view: "overview" })).toBe("/forge/p/sample_shop");
    expect(sampleHrefForDemo({ view: "traces", trace: "abc", tour: "forge:fg-hops" })).toBe(
      "/forge/p/sample_shop/observability/traces?trace=abc&tour=forge%3Afg-hops",
    );
  });

  it("drops the panel's own keys, which name nothing on the console", () => {
    expect(sampleHrefForDemo({ section: "built", browser: "data", view: "errors", issue: undefined })).toBe("/forge/p/sample_shop/observability/errors");
  });
});

function renderAt(url: string) {
  const rootRoute = createRootRoute();
  const demo = createRoute({ getParentRoute: () => rootRoute, path: "/forge/demo", component: ForgeDemoEntry });
  const console = createRoute({ getParentRoute: () => rootRoute, path: "/forge/p/$project/$", component: () => <div data-testid="sample-console" /> });
  const overview = createRoute({ getParentRoute: () => rootRoute, path: "/forge/p/$project", component: () => <div data-testid="sample-console" /> });
  const router = createRouter({ routeTree: rootRoute.addChildren([demo, console, overview]), history: createMemoryHistory({ initialEntries: [url] }) });
  render(<RouterProvider router={router} />);
  return router;
}

describe("ForgeDemoEntry", () => {
  const initial = useAuthStore.getState();
  afterEach(() => useAuthStore.setState(initial, true));

  it("signed in, opens the page the link names on the sample app", async () => {
    useAuthStore.setState({ initialized: true, user: { id: "u1" } as never });
    const router = renderAt("/forge/demo?view=traces&tour=forge:fg-trace");
    await waitFor(() => expect(router.state.location.pathname).toBe("/forge/p/sample_shop/observability/traces"));
    expect(router.state.location.search).toMatchObject({ tour: "forge:fg-trace" });
    expect(screen.queryByTestId("demo-panel")).toBeNull();
  });

  it("signed out, keeps the demo panel: there is no shell to walk", async () => {
    useAuthStore.setState({ initialized: true, user: null });
    const router = renderAt("/forge/demo?view=traces");
    await screen.findByTestId("demo-panel");
    expect(router.state.location.pathname).toBe("/forge/demo");
  });

  it("shows nothing until the session is known, so a signed-in person never sees the panel flash", async () => {
    useAuthStore.setState({ initialized: false, user: null });
    renderAt("/forge/demo");
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByTestId("demo-panel")).toBeNull();
  });
});
