// Copyright (c) 2025 Reliant Labs

/**
 * /apps as one table: an app per row, the site it opens at (its custom domain
 * once live), each environment's health and the last deploy — and no API
 * links, which are not a destination for a person.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, within } from "@testing-library/react";

import type { AppEnv, AppSummary } from "@/services/forge/apps";

const state = vi.hoisted(() => ({
  apps: [] as AppSummary[],
  loading: false,
  availability: "available",
  search: {} as Record<string, string>,
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
  useSearch: () => state.search,
}));
vi.mock("@/hooks/apps-queries", () => ({
  useApps: () => ({ apps: state.apps, loading: state.loading, availability: state.availability, detail: "" }),
}));
vi.mock("../../Layout/AreaShell", () => ({ AreaShell: ({ children }: { children: React.ReactNode }) => <>{children}</> }));
vi.mock("../startBuilding", () => ({ useStartBuildingApp: () => vi.fn() }));

import { AppsPage } from "../AppsPage";

const env = (name: string, over: Partial<AppEnv> = {}): AppEnv => ({
  id: `${name}-id`,
  name,
  preview: false,
  verdict: "converged",
  release: "v1.4.0",
  deployedAt: "2026-10-20T10:00:00Z",
  links: [],
  loading: false,
  ...over,
});

const STOREFRONT: AppSummary = {
  name: "storefront",
  envs: [
    env("prod", { links: [{ workload: "web", url: "https://shop.example.com", host: "shop.example.com", custom: true }] }),
    env("staging", { verdict: "converging", links: [{ workload: "web", url: "https://quiet-finch.reliantapps.dev", host: "quiet-finch.reliantapps.dev", custom: false }] }),
  ],
  previews: 2,
  lastDeployedAt: "2026-10-20T10:00:00Z",
};
const API_ONLY: AppSummary = { name: "ledger", envs: [env("prod")], previews: 0, lastDeployedAt: "2026-10-19T10:00:00Z" };
const FRESH: AppSummary = { name: "newsletter", envs: [], previews: 0 };

beforeEach(() => {
  state.apps = [STOREFRONT, API_ONLY, FRESH];
  state.loading = false;
  state.availability = "available";
  state.search = {};
});

describe("the Apps page", () => {
  it("is one accessible table, a row per app, each opening the app's console", () => {
    render(<AppsPage />);
    const table = screen.getByRole("table", { name: "Apps" });
    expect(within(table).getAllByRole("columnheader").map((th) => th.textContent)).toEqual(["App", "Site", "Environments", "Last deploy"]);
    const row = screen.getByTestId("app-row-storefront");
    expect(within(row).getByRole("link", { name: "storefront" })).toHaveAttribute("href", "/forge/p/storefront");
    expect(screen.getAllByRole("row")).toHaveLength(4);
  });

  it("links the site at prod's address — the custom domain when that is the one live", () => {
    render(<AppsPage />);
    const site = within(screen.getByTestId("app-row-storefront")).getByTestId("app-live-link");
    expect(site).toHaveAttribute("href", "https://shop.example.com");
    expect(site).toHaveAttribute("target", "_blank");
    expect(site).toHaveAttribute("data-custom-domain", "true");
  });

  it("shows every environment's health and counts previews", () => {
    render(<AppsPage />);
    const row = screen.getByTestId("app-row-storefront");
    expect(within(row).getByTestId("app-env-prod")).toHaveAttribute("data-verdict", "converged");
    expect(within(row).getByTestId("app-env-staging")).toHaveAttribute("data-verdict", "converging");
    expect(row).toHaveTextContent("+2 previews");
  });

  it("never links an API, and says why there is nothing to open", () => {
    render(<AppsPage />);
    const ledger = screen.getByTestId("app-row-ledger");
    expect(within(ledger).queryByTestId("app-live-link")).toBeNull();
    expect(ledger).toHaveTextContent("No public site");
    expect(screen.getByTestId("app-row-newsletter")).toHaveTextContent("Not deployed yet");
  });

  it("does not claim 'not deployed' when deployments could not be read", () => {
    state.availability = "unreachable";
    render(<AppsPage />);
    expect(screen.getByTestId("app-row-newsletter")).toHaveTextContent("Unavailable");
    expect(screen.getByTestId("app-row-newsletter")).not.toHaveTextContent("Not deployed yet");
  });

  it("shows skeleton rows while loading, and the build prompt when there is nothing", () => {
    state.loading = true;
    state.apps = [];
    const { unmount } = render(<AppsPage />);
    expect(screen.getByTestId("apps-list")).toHaveAttribute("aria-busy", "true");
    expect(screen.getAllByText("Loading…").length).toBeGreaterThan(0);
    unmount();
    state.loading = false;
    render(<AppsPage />);
    expect(screen.getByTestId("apps-empty")).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("highlights the app a claim just made the user's own", () => {
    state.search = { claimed: "storefront" };
    render(<AppsPage />);
    expect(screen.getByTestId("app-row-storefront")).toHaveAttribute("data-highlighted", "true");
    expect(screen.getByTestId("apps-claimed")).toHaveTextContent("storefront is yours now");
  });
});
