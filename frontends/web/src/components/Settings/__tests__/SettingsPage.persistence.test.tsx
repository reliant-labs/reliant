import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useParams,
  useSearch,
} from "@tanstack/react-router";
import { createSettingsSectionRoutes } from "../../../settingsSectionRoutes";
import { settingsSearchSchema } from "../../../routeSchemas";
import { SettingsPage } from "../SettingsPage";
import { readSettingsDestination } from "../settingsDestination";

// Keep the real page, routing and persistence; section contents and chrome
// aren't involved in choosing/restoring a destination.
vi.mock("../SettingsViewerTab", () => ({
  SettingsViewerTab: ({ section, onSectionChange, onClose }: {
    section: string;
    onSectionChange: (section: string) => void;
    onClose: () => void;
  }) => {
    const params = useParams({ strict: false });
    const search = useSearch({ strict: false });
    return <div>
      <output data-testid="destination">{JSON.stringify({ section, params, search })}</output>
      <button onClick={() => onSectionChange("appearance")}>Appearance</button>
      <button onClick={onClose}>Close</button>
    </div>;
  },
}));

function renderAt(path: string) {
  const root = createRootRoute({ component: Outlet });
  const home = createRoute({ getParentRoute: () => root, path: "/", component: () => <div>App</div> });
  const bare = createRoute({
    getParentRoute: () => root,
    path: "/settings",
    validateSearch: settingsSearchSchema,
    component: SettingsPage,
  });
  const router = createRouter({
    routeTree: root.addChildren([home, bare, ...createSettingsSectionRoutes(() => root, { page: SettingsPage })]),
    history: createMemoryHistory({ initialEntries: ["/", path] }),
  });
  const view = render(<RouterProvider router={router} />);
  return { router, ...view };
}

async function destination() {
  return JSON.parse((await screen.findByTestId("destination")).textContent!);
}

beforeEach(() => {
  localStorage.clear();
});

describe("remembering the settings destination", () => {
  it("falls back to account on first visit, replacing the bare entry", async () => {
    const { router } = renderAt("/settings");
    expect(await destination()).toMatchObject({ section: "account" });
    expect(router.state.location.pathname).toBe("/settings/account");
    act(() => router.history.back());
    await screen.findByText("App");
  });

  it("remembers section navigation after leaving and across a fresh mount", async () => {
    const first = renderAt("/settings/account");
    await destination();
    fireEvent.click(screen.getByText("Appearance"));
    await waitFor(() => expect(first.router.state.location.pathname).toBe("/settings/appearance"));
    await waitFor(() => expect(readSettingsDestination()).toEqual({ section: "appearance" }));
    fireEvent.click(screen.getByText("Close"));
    await screen.findByText("App");
    first.unmount();

    const second = renderAt("/settings");
    expect(await destination()).toMatchObject({ section: "appearance" });
    expect(second.router.state.location.pathname).toBe("/settings/appearance");
  });

  it.each(["ai?tab=models", "billing?tab=plans", "machines/d-1"])(
    "restores detail/subtab %s without replaying transient search params",
    async (path) => {
      const first = renderAt(`/settings/${path}${path.includes("?") ? "&" : "?"}checkout=success&planId=old&from=forge&returnTo=%2Fforge&notFound=old`);
      await destination();
      await waitFor(() => expect(readSettingsDestination().section).toBe(path.split(/[/?]/)[0]));
      first.unmount();
      const { router } = renderAt("/settings");
      await destination();
      await waitFor(() => expect(router.state.location.href).toBe(`/settings/${path}`));
      expect(router.state.location.search).not.toHaveProperty("checkout");
      expect(router.state.location.search).not.toHaveProperty("returnTo");
    },
  );

  it("honors an explicit machine link over the remembered section", async () => {
    localStorage.setItem("reliant.settings.lastDestination", JSON.stringify({ section: "billing", tab: "plans" }));
    const { router } = renderAt("/settings/machines/explicit");
    expect(await destination()).toMatchObject({ section: "machines", params: { machineId: "explicit" } });
    expect(router.state.location.pathname).toBe("/settings/machines/explicit");
    await waitFor(() => expect(readSettingsDestination()).toEqual({ section: "machines", machineId: "explicit" }));
  });

  it("lets this visit's explicit tab override the saved tab", async () => {
    localStorage.setItem("reliant.settings.lastDestination", JSON.stringify({ section: "billing", tab: "plans" }));
    const { router } = renderAt("/settings?tab=usage");
    expect(await destination()).toMatchObject({ section: "billing", search: { tab: "usage" } });
    expect(router.state.location.href).toBe("/settings/billing?tab=usage");
  });

  it.each(['{"section":"removed"}', "broken json"])("falls back safely with an invalid saved destination %s", async (saved) => {
    localStorage.setItem("reliant.settings.lastDestination", saved);
    renderAt("/settings");
    expect(await destination()).toMatchObject({ section: "account" });
  });

  it("lets explicit paths and subtabs win and tracks browser history", async () => {
    localStorage.setItem("reliant.settings.lastDestination", JSON.stringify({ section: "machines", machineId: "old" }));
    const { router } = renderAt("/settings/ai?tab=local");
    expect(await destination()).toMatchObject({ section: "ai", search: { tab: "local" } });
    await waitFor(() => expect(readSettingsDestination()).toEqual({ section: "ai", tab: "local" }));
    await act(() => router.navigate({ to: "/settings/billing", search: { tab: "usage" } }));
    await waitFor(() => expect(readSettingsDestination()).toEqual({ section: "billing", tab: "usage" }));
    act(() => router.history.back());
    await waitFor(() => expect(readSettingsDestination()).toEqual({ section: "ai", tab: "local" }));
  });

  it("retains an unknown-section notice without claiming account is shown", async () => {
    localStorage.setItem("reliant.settings.lastDestination", JSON.stringify({ section: "appearance" }));
    const { router } = renderAt("/settings/typo");
    expect(await destination()).toMatchObject({ section: "appearance" });
    const notice = screen.getByText(/There is no settings page called/).closest('[role="status"]');
    expect(notice).toHaveTextContent("typo");
    expect(notice).not.toHaveTextContent("account");
    fireEvent.click(screen.getByRole("button", { name: "Dismiss notice" }));
    await waitFor(() => expect(router.state.location.search).not.toHaveProperty("notFound"));
    expect(router.state.location.pathname).toBe("/settings/appearance");
  });

  it("continues to render and navigate when storage reads and writes throw", async () => {
    const get = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("blocked"); });
    const set = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("full"); });
    try {
      const { router } = renderAt("/settings");
      expect(await destination()).toMatchObject({ section: "account" });
      fireEvent.click(screen.getByText("Appearance"));
      await waitFor(() => expect(router.state.location.pathname).toBe("/settings/appearance"));
    } finally {
      get.mockRestore();
      set.mockRestore();
    }
  });
});
