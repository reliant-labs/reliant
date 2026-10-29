import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, waitFor } from "@testing-library/react";
import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router";
import { setRuntimeConfig } from "@/test/runtimeConfig";

// The tracker rides the real trackEvent, so these tests also pin that the
// consent and dev gates apply to page_visited exactly as to every other event.

const consent = vi.hoisted(() => ({ analyticsEnabled: true }));
vi.mock("../../hooks/privacy-queries", () => ({
  privacyConsent: () => consent,
}));
vi.mock("../../lib/constants", () => ({ isDev: false }));
vi.mock("@reliantlabs/forge-web-runtime", () => ({ track: vi.fn() }));

import { track } from "@reliantlabs/forge-web-runtime";
import { PageVisitTracker } from "../PageVisitTracker";
import { routeTemplate } from "../../lib/pageVisit";

function makeRouter(initial: string) {
  const root = createRootRoute({
    component: () => (
      <>
        <PageVisitTracker />
        <Outlet />
      </>
    ),
  });
  const leaf = (path: string) =>
    createRoute({ getParentRoute: () => root, path, component: () => null });
  const layout = createRoute({
    getParentRoute: () => root,
    id: "_app",
    component: () => <Outlet />,
  });
  const index = createRoute({ getParentRoute: () => layout, path: "/", component: () => null });
  const project = createRoute({
    getParentRoute: () => layout,
    path: "project/$projectId",
    component: () => null,
  });
  return createRouter({
    routeTree: root.addChildren([
      layout.addChildren([index, project]),
      leaf("/settings"),
      leaf("/claim"),
    ]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  });
}

const pageEvents = () =>
  vi.mocked(track).mock.calls.filter(([name]) => name === "product.page_visited");

describe("PageVisitTracker", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    consent.analyticsEnabled = true;
    setRuntimeConfig({ OTEL_ENDPOINT: "https://ingest.example.dev/k/rbk_x" });
  });

  it("sends the route template, never the URL with ids, query or hash", async () => {
    const router = makeRouter("/project/9f1c2d3e-secret-id?token=abc#rcl_claimtoken");
    render(<RouterProvider router={router} />);

    await waitFor(() => expect(pageEvents()).toHaveLength(1));
    expect(pageEvents()[0][1]).toEqual({ page_name: "/project/$projectId" });
    expect(JSON.stringify(vi.mocked(track).mock.calls)).not.toMatch(
      /9f1c2d3e|token=abc|rcl_claimtoken/,
    );
  });

  it("records the previous page on navigation and one event per visit", async () => {
    const router = makeRouter("/");
    render(<RouterProvider router={router} />);
    await waitFor(() => expect(pageEvents()).toHaveLength(1));
    expect(pageEvents()[0][1]).toEqual({ page_name: "/" });

    await act(() => router.navigate({ to: "/settings" }));
    await waitFor(() => expect(pageEvents()).toHaveLength(2));
    expect(pageEvents()[1][1]).toEqual({ page_name: "/settings", previous_page: "/" });

    await act(() => router.navigate({ to: "/project/$projectId", params: { projectId: "abc" } }));
    await waitFor(() => expect(pageEvents()).toHaveLength(3));
    expect(pageEvents()[2][1]).toEqual({
      page_name: "/project/$projectId",
      previous_page: "/settings",
    });
  });

  it("does not count a search-only change as a visit", async () => {
    const router = makeRouter("/settings");
    render(<RouterProvider router={router} />);
    await waitFor(() => expect(pageEvents()).toHaveLength(1));

    await act(() => router.navigate({ to: "/settings", search: { tab: "x" } as never }));
    expect(pageEvents()).toHaveLength(1);
  });

  it("sends nothing when the user has opted out of analytics", async () => {
    consent.analyticsEnabled = false;
    const router = makeRouter("/settings");
    render(<RouterProvider router={router} />);
    await act(() => router.navigate({ to: "/claim" }));

    expect(pageEvents()).toHaveLength(0);
  });

  it("sends nothing in a build with no telemetry endpoint", async () => {
    setRuntimeConfig({ OTEL_ENDPOINT: "" });
    render(<RouterProvider router={makeRouter("/settings")} />);
    await act(async () => {});

    expect(pageEvents()).toHaveLength(0);
  });
});

describe("routeTemplate", () => {
  it("keeps the root, trims a trailing slash, defaults an unmatched leaf to the root", () => {
    expect(routeTemplate("/")).toBe("/");
    expect(routeTemplate("/settings/")).toBe("/settings");
    expect(routeTemplate("/workflow/$workflowName")).toBe("/workflow/$workflowName");
    expect(routeTemplate(undefined)).toBe("/");
  });
});
