import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { switchTarget } from "../ConsoleLayout";
import { ConsoleProjectSwitcher } from "../ConsoleProjectSwitcher";

const orgApps = vi.hoisted(() => ({ current: { apps: [] as { name: string }[], loading: false, availability: "available", detail: "" } }));
vi.mock("@/hooks/apps-queries", () => ({ useApps: () => orgApps.current }));
// The selector's own reads: no catalog here, so it lists the control plane's apps it is given.
vi.mock("@/hooks/project-catalog-queries", () => ({
  useProjectCatalog: () => ({ catalog: undefined, isLoading: false, error: new Error("no catalog here"), refetch: () => {} }),
}));
vi.mock("@/hooks/project-queries", async (original) => ({
  ...(await original<typeof import("@/hooks/project-queries")>()),
  useProjects: () => ({ projects: [], isLoading: false, error: null, refetch: () => {} }),
}));
vi.mock("@/hooks/sidebar-project-queries", () => ({
  useSidebarProjectPrefs: () => ({ prefs: { pinned: new Set(), hidden: new Set(), showAll: false } }),
}));
vi.mock("@/components/Apps/startBuilding", () => ({ useStartBuildingApp: () => () => {} }));
vi.mock("@tanstack/react-router", async (orig) => ({ ...(await orig<object>()), useNavigate: () => vi.fn() }));

describe("switchTarget", () => {
  it("keeps the same page on the other app", () => {
    expect(switchTarget("shop", { kind: "releases" })).toBe("/forge/p/shop/releases");
  });

  it("lands on the list when the page is about something the other app lacks", () => {
    expect(switchTarget("shop", { kind: "environment", env: "prod" })).toBe(
      switchTarget("shop", { kind: "environments" })
    );
    expect(switchTarget("shop", { kind: "workload", env: "prod", workload: "api" } as never)).toBe(
      switchTarget("shop", { kind: "overview" })
    );
  });
});

describe("ConsoleProjectSwitcher", () => {
  const apps = (...names: string[]) => ({
    apps: names.map((name) => ({ name })),
    loading: false,
    availability: "available",
    detail: "",
  });

  it("lists the org's apps, with the current one checked", () => {
    orgApps.current = apps("shop", "api");
    render(<ConsoleProjectSwitcher forgeProject="shop" onSelect={vi.fn()} onSelectAll={vi.fn()} />);
    fireEvent.click(screen.getByTestId("forge-project-switcher"));
    const items = screen.getAllByTestId(/^project-selector-app-/).map((item) => item.dataset.testid);
    expect(items).toEqual(["project-selector-app-shop", "project-selector-app-api"]);
    expect(screen.getByTestId("project-selector-app-shop")).toHaveAttribute("aria-selected", "true");
  });

  it("says other apps are unavailable when the control plane cannot be read", () => {
    orgApps.current = { ...apps(), availability: "no-access" };
    render(<ConsoleProjectSwitcher forgeProject="shop" onSelect={vi.fn()} onSelectAll={vi.fn()} />);
    fireEvent.click(screen.getByTestId("forge-project-switcher"));
    expect(screen.getByText("Other apps are unavailable right now.")).toBeInTheDocument();
  });

  it("keeps the URL's app listed when the control plane has not", () => {
    orgApps.current = apps("api");
    render(<ConsoleProjectSwitcher forgeProject="local-only" onSelect={vi.fn()} onSelectAll={vi.fn()} />);
    fireEvent.click(screen.getByTestId("forge-project-switcher"));
    expect(screen.getByTestId("project-selector-app-local-only")).toBeInTheDocument();
  });

  it("selects an app by name and offers All apps", () => {
    orgApps.current = apps("api", "shop");
    const onSelect = vi.fn();
    const onSelectAll = vi.fn();
    render(<ConsoleProjectSwitcher forgeProject="shop" onSelect={onSelect} onSelectAll={onSelectAll} />);
    fireEvent.click(screen.getByTestId("forge-project-switcher"));
    fireEvent.click(screen.getByTestId("project-selector-app-api"));
    expect(onSelect).toHaveBeenCalledWith("api");
    fireEvent.click(screen.getByTestId("forge-project-switcher"));
    fireEvent.click(screen.getByTestId("forge-project-menu-all-apps"));
    expect(onSelectAll).toHaveBeenCalled();
  });
});
