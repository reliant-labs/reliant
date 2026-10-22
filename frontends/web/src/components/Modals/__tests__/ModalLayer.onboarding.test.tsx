/**
 * ModalLayer must respect the route it is rendering over.
 *
 * The layer is mounted on the root route so modals survive navigation, which
 * also means it renders on `/onboarding` — a `fixed inset-0 z-40` overlay that
 * every modal (z-50) outranks. These tests pin that an api-key-setup modal
 * raised while onboarding is active stays hidden, and comes back once the user
 * is somewhere it belongs.
 */
/* eslint-disable @typescript-eslint/no-explicit-any */
import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

vi.mock("../../ApiKeySetupModal", () => ({
  ApiKeySetupModal: () => <div data-testid="api-key-setup-modal" />,
}));
vi.mock("../../UpgradeRequiredModal", () => ({
  UpgradeRequiredModal: () => <div data-testid="upgrade-required-modal" />,
}));
vi.mock("../../BillingEmailRequiredModal", () => ({
  BillingEmailRequiredModal: () => (
    <div data-testid="billing-email-required-modal" />
  ),
}));

import { ModalLayer } from "../ModalLayer";
import { useModalStore } from "@/store/modalStore";

/**
 * Renders ModalLayer under a real router at `pathname`, and does not return
 * until the route has actually resolved.
 *
 * The await matters. RouterProvider mounts asynchronously, so a synchronous
 * `queryByTestId` immediately after `render` finds nothing whether or not the
 * modal was suppressed — a "modal is absent" assertion would pass vacuously
 * even with the route policy deleted. Waiting for the route marker proves the
 * tree is mounted, so a later absence is a real absence.
 */
async function renderAt(pathname: string) {
  const rootRoute = createRootRoute({
    component: () => (
      <>
        <Outlet />
        <ModalLayer />
      </>
    ),
  });
  const routes = ["/", "/onboarding", "/settings"].map((path) =>
    createRoute({
      getParentRoute: () => rootRoute,
      path,
      component: () => <div data-testid="route-ready" />,
    }),
  );
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: [pathname] }),
  });
  const result = render(<RouterProvider router={router as any} />);
  await screen.findByTestId("route-ready");
  return result;
}

describe("ModalLayer route awareness", () => {
  beforeEach(() => {
    useModalStore.setState({ activeModal: null, data: undefined });
  });

  it("does not render the api-key-setup modal on /onboarding", async () => {
    useModalStore.getState().openModal("api-key-setup");
    await renderAt("/onboarding");

    expect(
      screen.queryByTestId("api-key-setup-modal"),
    ).not.toBeInTheDocument();
  });

  it("renders it on a route that does not own the screen", async () => {
    useModalStore.getState().openModal("api-key-setup");
    await renderAt("/");

    expect(await screen.findByTestId("api-key-setup-modal")).toBeInTheDocument();
  });

  it("suppresses rather than clears, so the request is not silently dropped", async () => {
    useModalStore.getState().openModal("api-key-setup");
    await renderAt("/onboarding");

    // Still "open" in the store — it simply has nowhere legitimate to draw.
    // Leaving /onboarding re-renders it if it is still relevant.
    expect(useModalStore.getState().activeModal).toBe("api-key-setup");
  });

  it("still renders billing modals on /onboarding", async () => {
    useModalStore.getState().openModal("billing-email-required", {
      message: "billing email required",
    });
    await renderAt("/onboarding");

    expect(
      await screen.findByTestId("billing-email-required-modal"),
    ).toBeInTheDocument();
  });
});
