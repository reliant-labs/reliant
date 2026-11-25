// Copyright (c) 2026 Reliant Labs

/**
 * The reported bug, end to end: "clicking Settings goes to Agents › Sandboxes,
 * not the settings page, so you can never reach anything in Settings."
 *
 * The real hybrid nav and the real SettingsPage share one router here, so the
 * click goes through every piece that decides where it lands: the Settings
 * row's href, the bare /settings restore (settingsDestination), and which area
 * takes the nav column on the page it lands on. The unit tests beside each
 * piece (settingsDestination.test, SettingsPage.persistence.test, the nav's
 * navConfig.test) each passed while the pieces together still stranded the
 * user, so this pins the whole path.
 */

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useRouter,
  useRouterState,
} from "@tanstack/react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { machineSettingsPath, sandboxesPath, settingsPath } from "@/lib/paths";

import { settingsSearchSchema } from "../../../routeSchemas";
import { createSettingsSectionRoutes } from "../../../settingsSectionRoutes";
import { hybridNav, type NavCtx } from "../../Layout/sidebar/navConfig";
import { NavRenderer } from "../../Layout/sidebar/NavRenderer";
import { SettingsPage } from "../SettingsPage";

vi.mock("@/lib/changesFeature", () => ({ changesUIEnabled: () => false }));

// The sections' contents are not what decides where Settings opens; the page,
// its routes and its persistence stay real.
vi.mock("../SettingsViewerTab", () => ({
  SettingsViewerTab: ({ section }: { section: string }) => <output data-testid="settings-section">{section}</output>,
}));

/** The shell's nav, fed from the router the way ShellNavBody feeds it. */
function Nav() {
  const router = useRouter();
  const location = useRouterState({ select: (state) => state.location });
  const ctx: NavCtx = {
    pathname: location.pathname,
    search: location.search as Record<string, unknown>,
    mode: "one",
    projectId: "p1",
    projectName: "shopfront",
    app: null,
    console: null,
    page: null,
    orgPage: null,
  };
  return <NavRenderer config={hybridNav} ctx={ctx} slots={{}} onNavigate={(href) => void router.navigate({ href })} />;
}

function renderAt(path: string) {
  const root = createRootRoute({
    component: () => (
      <>
        <Nav />
        <Outlet />
      </>
    ),
  });
  const home = createRoute({ getParentRoute: () => root, path: "/", component: () => <div>App</div> });
  const bare = createRoute({
    getParentRoute: () => root,
    path: "/settings",
    validateSearch: settingsSearchSchema,
    component: SettingsPage,
  });
  const router = createRouter({
    routeTree: root.addChildren([home, bare, ...createSettingsSectionRoutes(() => root, { page: SettingsPage })]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

const navArea = () => screen.getByTestId("nav-drill-in").getAttribute("data-area");

async function clickSettings(router: ReturnType<typeof renderAt>, expected: string) {
  fireEvent.click(await screen.findByTestId("sidebar-settings-button"));
  await waitFor(() => expect(router.state.location.pathname).toBe(expected));
  // Settings' own sections take the nav column: every other setting is a click away.
  await waitFor(() => expect(navArea()).toBe("settings"));
  expect(screen.getByTestId("settings-section")).toHaveTextContent(expected.split("/").pop()!);
  expect(screen.getByTestId("sidebar-settings-button")).toHaveAttribute("aria-current", "page");
}

beforeEach(() => {
  localStorage.clear();
});

describe("clicking Settings", () => {
  it.each([sandboxesPath(), machineSettingsPath("d-1")])(
    "from %s opens Settings' own page and sections, not Agents › Sandboxes",
    async (start) => {
      const router = renderAt(start);
      await waitFor(() => expect(navArea()).toBe("agents"));
      expect(screen.getByTestId("nav-sandboxes")).toHaveAttribute("aria-current", "page");

      await clickSettings(router, settingsPath("account"));
      expect(screen.queryByTestId("nav-sandboxes")).not.toBeInTheDocument();
    },
  );

  // What a browser that visited Agent sandboxes before the fix still holds.
  it.each([
    ["Agent sandboxes", { section: "machines" }],
    ["one machine", { section: "machines", machineId: "d-1" }],
  ])("opens Settings when %s is the remembered settings page", async (_name, saved) => {
    localStorage.setItem("reliant.settings.lastDestination", JSON.stringify(saved));
    const router = renderAt("/");
    await screen.findByText("App");
    await clickSettings(router, settingsPath("account"));
  });

  it("reaches every Settings section from the nav, and comes back to the last one after Agent sandboxes", async () => {
    const router = renderAt("/");
    await screen.findByText("App");
    await clickSettings(router, settingsPath("account"));

    for (const section of ["appearance", "privacy", "shortcuts"] as const) {
      fireEvent.click(screen.getByTestId(`nav-settings-${section}`));
      await waitFor(() => expect(router.state.location.pathname).toBe(settingsPath(section)));
      expect(screen.getByTestId("settings-section")).toHaveTextContent(section);
      expect(screen.getByTestId(`nav-settings-${section}`)).toHaveAttribute("aria-current", "page");
      expect(navArea()).toBe("settings");
    }

    await act(() => router.navigate({ href: sandboxesPath() }));
    await waitFor(() => expect(navArea()).toBe("agents"));

    await clickSettings(router, settingsPath("shortcuts"));
  });
});
