/**
 * The /settings/$section routes, mounted from the SAME definitions routes.tsx
 * uses (createSettingsSectionRoutes) with a stub page:
 *
 *   - an unknown slug redirects to /settings with a notice instead of
 *     crashing the app to the error screen (it used to: parseParams threw a
 *     ZodError, which is a route error, not a not-found);
 *   - `machines` — the nav label of the `environments` section — is an alias;
 *   - one machine's detail view is a real URL, and the older `?daemon=<id>`
 *     link is moved onto it.
 */

import { describe, expect, it } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useLocation,
  useParams,
} from "@tanstack/react-router";

import { createSettingsSectionRoutes } from "./settingsSectionRoutes";
import { settingsSearchSchema } from "./routeSchemas";

function PageStub() {
  const params = useParams({ strict: false }) as { section?: string; machineId?: string };
  const location = useLocation();
  return (
    <div data-testid="page">
      {JSON.stringify({ section: params.section, machineId: params.machineId, search: location.search })}
    </div>
  );
}

function ErrorStub() {
  return <div data-testid="route-error">route error</div>;
}

function renderAt(path: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet />, errorComponent: ErrorStub });
  const authed = createRoute({
    getParentRoute: () => rootRoute,
    id: "_authenticated",
    component: () => <Outlet />,
  });
  const bare = createRoute({
    getParentRoute: () => authed,
    path: "/settings",
    validateSearch: settingsSearchSchema,
    component: PageStub,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      authed.addChildren([bare, ...createSettingsSectionRoutes(() => authed, { page: PageStub })]),
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

async function page() {
  return JSON.parse((await screen.findByTestId("page")).textContent ?? "{}");
}

describe("settings section routes", () => {
  it("renders a known section", async () => {
    renderAt("/settings/mcp");
    expect(await page()).toMatchObject({ section: "mcp" });
  });

  it("redirects an unknown section to Settings with a not-found notice, not an error", async () => {
    const router = renderAt("/settings/foo");
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings"));
    expect((await page()).search).toEqual({ notFound: "foo" });
    expect(screen.queryByTestId("route-error")).not.toBeInTheDocument();
  });

  it("treats /settings/machines as the Machines section", async () => {
    const router = renderAt("/settings/machines?from=connectors");
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/environments"));
    expect(await page()).toMatchObject({ section: "environments", search: { from: "connectors" } });
  });

  it("opens one machine at /settings/environments/$machineId", async () => {
    renderAt("/settings/environments/d-1");
    expect(await page()).toMatchObject({ machineId: "d-1" });
  });

  it("moves the older ?daemon=<id> link onto the path, keeping other params", async () => {
    const router = renderAt("/settings/environments?daemon=d-2&from=connectors");
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/environments/d-2"));
    expect(await page()).toMatchObject({ machineId: "d-2", search: { from: "connectors" } });
  });
});
