// Copyright (c) 2025 Reliant Labs

import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import type { AppChipModel, ProjectCatalogRow } from "@/services/projects/catalog";

import { AppChip } from "../AppChip";
import { checkoutText, ProjectRowSummary } from "../ProjectRowSummary";

afterEach(cleanup);

function inRouter(ui: ReactNode) {
  const root = createRootRoute({ component: () => <>{ui}</> });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  return render(<RouterProvider router={router as never} />);
}

const hosted = { name: "shop", createdAt: "", lastDeployedAt: "2026-01-01T00:00:00Z", environments: ["prod"], hasStaticSite: false, sources: [], warning: "" };
const LIVE: AppChipModel = { name: "shop", link: "source", state: "live", hosted, isStaticSite: false, environments: ["prod"] };
const NOT_HOSTED: AppChipModel = { name: "admin", link: "not_hosted", state: "not_deployed", isStaticSite: false, environments: [] };
const SITE: AppChipModel = { ...LIVE, name: "landing", isStaticSite: true, hosted: { ...hosted, name: "landing", hasStaticSite: true } };

describe("AppChip", () => {
  it("links a hosted app into its console and names its state", async () => {
    inRouter(<AppChip app={LIVE} />);
    const chip = await screen.findByRole("link", { name: "shop: deployed" });
    expect(chip.getAttribute("href")).toBe("/forge/p/shop");
  });

  it("is plain text for an app the org does not have", async () => {
    inRouter(<AppChip app={NOT_HOSTED} />);
    expect(await screen.findByRole("img", { name: "admin: not deployed" })).toBeTruthy();
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("is plain text when not interactive, and says a site is one", async () => {
    inRouter(<AppChip app={SITE} interactive={false} />);
    expect(await screen.findByRole("img", { name: "landing: deployed, static site" })).toBeTruthy();
  });
});

function row(over: Partial<ProjectCatalogRow> = {}): ProjectCatalogRow {
  return {
    kind: "project",
    key: "p-1",
    name: "Shop",
    activityAt: 0,
    project: { id: "p-1", name: "Shop", path: "/src/shop", is_git_repo: true, is_forge: true, last_active: "", created_at: "", updated_at: "" },
    shared: false,
    repos: [{ id: "r", name: "shop", relativePath: "", remote: "github.com/acme/shop" }],
    apps: [LIVE, NOT_HOSTED, SITE, { ...NOT_HOSTED, name: "x" }],
    checkouts: [],
    hasCheckout: false,
    ...over,
  };
}

describe("ProjectRowSummary", () => {
  it("shows the name, repos, where the code is and three chips then +N", async () => {
    inRouter(<ProjectRowSummary row={row({ checkouts: [{ daemonId: "d", daemonName: "laptop", installState: ProjectInstallState.INSTALLED }], hasCheckout: true })} pinned />);
    expect(await screen.findByText("Shop")).toBeTruthy();
    expect(screen.getByText("github.com/acme/shop")).toBeTruthy();
    expect(screen.getByText("On laptop")).toBeTruthy();
    expect(screen.getAllByTestId("app-chip")).toHaveLength(3);
    expect(screen.getByText("+1")).toBeTruthy();
    expect(screen.getByLabelText("Pinned")).toBeTruthy();
    // The row is the control: its chips are not links.
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("says an app-only row has no code here", async () => {
    inRouter(<ProjectRowSummary row={{ kind: "app", key: "app:solo", name: "solo", activityAt: 0, app: LIVE }} />);
    expect(await screen.findByText("App without code here")).toBeTruthy();
  });
});

describe("checkoutText", () => {
  it.each([
    [[], false, "Not on a machine"],
    [[{ daemonId: "d", daemonName: "box", installState: ProjectInstallState.INSTALLING }], false, "Cloning…"],
    [
      [
        { daemonId: "a", daemonName: "laptop", installState: ProjectInstallState.INSTALLED },
        { daemonId: "b", daemonName: "cloud", installState: ProjectInstallState.INSTALLED },
      ],
      false,
      "On laptop +1",
    ],
    [[], true, "Shared with you"],
  ] as const)("%j shared=%s: %s", (checkouts, shared, want) => {
    expect(checkoutText(row({ checkouts: [...checkouts], shared }))).toBe(want);
  });
});
