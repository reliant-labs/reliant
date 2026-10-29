// Copyright (c) 2025 Reliant Labs

/**
 * The environment page's Frontends panel: the live address of each static site
 * (the platform's default hostname — no custom domain needed), its release and
 * custom domains, and the states around it (loading, unreadable, none).
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import type { LiveEnv } from "@/services/forge/live";

import { FIXTURE_DOMAINS, FIXTURE_HOSTED } from "../../Workloads/__fixtures__/workloadFixtures";
import { OverviewTab, type OverviewTabProps } from "../EnvTabPanels";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const PROD = {
  id: "env-prod",
  name: "prod",
  project: "shop",
  kind: "persistent",
  declaredShape: null,
  declaredBy: null,
  release: "v1.7.15",
  releaseProvenance: null,
  promotedByActor: "ci",
  promotedByUserId: "",
  phase: "succeeded",
  observed: { state: "confirmed" },
  drift: { state: "in_sync" },
  driftDetail: "",
  provenance: "",
  holds: [],
} as unknown as LiveEnv;

const STATUS: CloudEnvStatus = {
  verdict: "converged",
  currentPromotion: null,
  workloads: [...FIXTURE_HOSTED.get("prod")!.values()],
};

function renderTab(over: Partial<OverviewTabProps> = {}) {
  const props: OverviewTabProps = {
    envName: "prod",
    live: PROD,
    liveLoading: false,
    forgeEnv: null,
    daemon: "offline",
    projectId: "proj-1",
    forgeProject: "shop",
    domains: { availability: "available", domains: FIXTURE_DOMAINS, detail: "" },
    domainsLoading: false,
    onOpenDomains: () => {},
    onManageDomains: () => {},
    workloadsHref: "/forge/p/shop?env=prod",
    cloudStatus: STATUS,
    cloudStatusLoading: false,
    domainsHref: "/forge/p/shop/domains",
    ...over,
  };
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })}>
      <OverviewTab {...props} />
    </QueryClientProvider>
  );
}

describe("the environment page's Frontends panel", () => {
  it("shows each site at the address it is opened at, with the release and an open link", () => {
    renderTab();
    const panel = screen.getByTestId("env-frontends");
    const web = within(panel).getByTestId("frontend-prod-web");
    // web's custom domain is live, so it leads; the generated address stays beside it.
    expect(within(web).getByTestId("frontend-url")).toHaveAttribute("href", "https://app.reliantlabs.io");
    expect(within(web).getByTestId("frontend-platform-url")).toHaveTextContent("valiant-caribou.sites.reliantlabs.dev");
    const docs = within(panel).getByTestId("frontend-prod-docs");
    expect(within(docs).getByTestId("frontend-url")).toHaveAttribute("href", "https://bright-otter.sites.reliantlabs.dev");
    expect(within(web).getByTestId("frontend-release")).toHaveTextContent("v1.7.15");
    expect(within(web).getAllByTestId("frontend-copy").length).toBeGreaterThan(0);
    // Services are not frontends.
    expect(within(panel).queryByTestId("frontend-prod-admin-server")).toBeNull();
  });

  it("links 'Add a domain' to the Domains screen", () => {
    renderTab({ domains: { availability: "available", domains: [], detail: "" } });
    const add = within(screen.getByTestId("frontend-prod-web")).getByTestId("frontend-add-domain");
    expect(add).toHaveTextContent("Add a domain");
    expect(add).toHaveAttribute("href", "/forge/p/shop/domains");
  });

  it("shows a skeleton while the deployments load, and says so when they could not be read", () => {
    const { unmount } = renderTab({ cloudStatus: undefined, cloudStatusLoading: true });
    expect(screen.getByTestId("frontends-loading")).toBeInTheDocument();
    unmount();
    renderTab({ cloudStatus: undefined, cloudStatusLoading: false });
    expect(screen.getByTestId("env-frontends-unavailable")).toHaveTextContent("could not read");
  });

  it("explains how to ship a frontend when the environment has none", () => {
    renderTab({ cloudStatus: { ...STATUS, workloads: STATUS.workloads.filter((w) => w.tier !== "static") } });
    expect(screen.getByTestId("frontends-empty")).toHaveTextContent("No frontend deployed here yet");
  });

  it("is not offered for an environment Reliant does not run", () => {
    renderTab({ live: { ...PROD, kind: "self_managed" } as LiveEnv });
    expect(screen.queryByTestId("env-frontends")).toBeNull();
  });
});
