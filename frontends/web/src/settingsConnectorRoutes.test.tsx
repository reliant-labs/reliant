/**
 * The connector routes under /settings/connectors, mounted from the SAME
 * definitions routes.tsx uses (createSettingsConnectorRoutes) with stub pages:
 *
 *   - /settings/connectors/authorize is the OAuth consent page third parties
 *     redirect to by exact URL (internal/mcpserver ConsentPath), so it must
 *     keep rendering — with its query string intact;
 *   - /settings/connectors, the retired settings section, redirects to
 *     Settings → Machines rather than 404ing;
 *   - the generic /settings/$section route is not shadowed.
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

import { createSettingsConnectorRoutes } from "./settingsConnectorRoutes";

function ConsentStub() {
  const location = useLocation();
  return <div data-testid="consent">{location.searchStr}</div>;
}

function SectionStub() {
  const params = useParams({ strict: false }) as { section?: string };
  return <div data-testid="section">{params.section}</div>;
}

function renderAt(path: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const authed = createRoute({
    getParentRoute: () => rootRoute,
    id: "_authenticated",
    component: () => <Outlet />,
  });
  const section = createRoute({
    getParentRoute: () => authed,
    path: "/settings/$section",
    component: SectionStub,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      authed.addChildren([
        ...createSettingsConnectorRoutes(() => authed, { consent: ConsentStub }),
        section,
      ]),
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

describe("settings connector routes", () => {
  it("still renders the consent page at /settings/connectors/authorize", async () => {
    const router = renderAt("/settings/connectors/authorize?client_id=abc&client_name=ChatGPT");
    expect(await screen.findByTestId("consent")).toHaveTextContent("client_id=abc");
    expect(router.state.location.pathname).toBe("/settings/connectors/authorize");
  });

  it("redirects the retired /settings/connectors section to Machines", async () => {
    const router = renderAt("/settings/connectors");
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/environments"));
    expect(await screen.findByTestId("section")).toHaveTextContent("environments");
  });

  it("leaves other settings sections alone", async () => {
    renderAt("/settings/mcp");
    expect(await screen.findByTestId("section")).toHaveTextContent("mcp");
  });
});
