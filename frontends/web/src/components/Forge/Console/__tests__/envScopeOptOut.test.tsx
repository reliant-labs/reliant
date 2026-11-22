import { cleanup, render, screen } from "@testing-library/react";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from "@tanstack/react-router";
import { afterEach, describe, expect, it } from "vitest";

import { useRouteUsesEnvScope } from "../useConsoleNavModel";

function Probe() {
  return <div data-testid="scoped">{String(useRouteUsesEnvScope())}</div>;
}

/** A console-like tree: a layout, a scoped page, and a page (and its child) that opted out. */
function renderAt(path: string) {
  const root = createRootRoute({ component: () => <Outlet /> });
  const layout = createRoute({ getParentRoute: () => root, id: "console", component: () => <><Probe /><Outlet /></> });
  const scoped = createRoute({ getParentRoute: () => layout, path: "/forge/p/$project/releases", component: () => null });
  const unscoped = createRoute({ getParentRoute: () => layout, path: "/forge/p/$project/tests", staticData: { envScope: false }, component: () => <Outlet /> });
  const unscopedChild = createRoute({ getParentRoute: () => unscoped, path: "$run", component: () => null });
  const router = createRouter({
    routeTree: root.addChildren([layout.addChildren([scoped, unscoped.addChildren([unscopedChild])])]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
}

afterEach(cleanup);

describe("the env-scope opt-out (staticData.envScope)", () => {
  it("is scoped on a page that declares nothing", async () => {
    renderAt("/forge/p/shop/releases");
    expect(await screen.findByTestId("scoped")).toHaveTextContent("true");
  });

  it("is unscoped on a page that opted out, and below it", async () => {
    renderAt("/forge/p/shop/tests");
    expect(await screen.findByTestId("scoped")).toHaveTextContent("false");
    cleanup();
    renderAt("/forge/p/shop/tests/run-1");
    expect(await screen.findByTestId("scoped")).toHaveTextContent("false");
  });
});
