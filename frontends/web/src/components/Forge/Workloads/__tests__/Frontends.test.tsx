// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's Frontends panel: every hosted environment's static sites next
 * to its services, each with its live address (the platform's, no custom
 * domain needed), a copy button, the release, its custom domains, and an "Add a
 * domain" path where there are none.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { FIXTURE_HOSTED } from "../__fixtures__/workloadFixtures";
import { renderOverview } from "./workloadsTestUtils";

vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const ready = async () => waitFor(() => expect(screen.getByTestId("overview")).toHaveAttribute("data-state", "ready"));

describe("the Overview's Frontends panel", () => {
  it("headlines a site's live custom domain, with the generated address beside it", async () => {
    renderOverview();
    await ready();
    const panel = screen.getByTestId("overview-frontends");
    const web = within(panel).getByTestId("frontend-prod-web");
    const link = within(web).getByTestId("frontend-url");
    expect(link).toHaveAttribute("href", "https://app.reliantlabs.io");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveTextContent("app.reliantlabs.io");
    expect(within(web).getByTestId("frontend-platform-url")).toHaveTextContent("valiant-caribou.sites.reliantlabs.dev");
    // The headline domain is not listed a second time among the domains.
    expect(within(web).queryByTestId("frontend-domain-app.reliantlabs.io")).toBeNull();
    expect(within(web).getByText("Static site")).toBeInTheDocument();
    expect(within(web).getByTestId("frontend-release")).toBeInTheDocument();
  });

  it("headlines the platform address while the custom domain is not live, and shows that domain's state", async () => {
    renderOverview();
    await ready();
    const docs = screen.getByTestId("frontend-prod-docs");
    expect(within(docs).getByTestId("frontend-url")).toHaveAttribute("href", "https://bright-otter.sites.reliantlabs.dev");
    expect(within(docs).queryByTestId("frontend-platform-url")).toBeNull();
    const pending = within(docs).getByTestId("frontend-domain-docs.reliantlabs.io");
    expect(within(pending).queryByRole("link", { name: /docs\.reliantlabs\.io/ })).toBeNull();
    expect(pending).toHaveTextContent("Waiting for DNS");
    // A redirect (www → app) is not one of the site's own addresses.
    expect(screen.queryByTestId("frontend-domain-www.reliantlabs.io")).toBeNull();
  });

  it("offers 'Add a domain' on a site with none, linking to the Domains screen, and 'Manage domains' on one with some", async () => {
    renderOverview({ env: "all" });
    await ready();
    const dev = screen.getByTestId("frontend-dev-web");
    expect(within(dev).getByTestId("frontend-no-domain")).toHaveTextContent("Serves on its generated address.");
    const add = within(dev).getByTestId("frontend-add-domain");
    expect(add).toHaveTextContent("Add a domain");
    expect(add).toHaveAttribute("href", "/domains");
    expect(within(screen.getByTestId("frontend-prod-web")).getByTestId("frontend-add-domain")).toHaveTextContent("Manage domains");
  });

  it("says a site has no address yet instead of leaving a blank", async () => {
    renderOverview({ env: "dev" });
    await ready();
    const dev = screen.getByTestId("frontend-dev-web");
    expect(within(dev).getByTestId("frontend-url-pending")).toHaveTextContent("No address yet");
    expect(within(dev).queryByTestId("frontend-url")).toBeNull();
  });

  it("marks a stopped site as stopped, not as healthy", async () => {
    renderOverview();
    await ready();
    const docs = screen.getByTestId("frontend-prod-docs");
    expect(within(docs).getByText("Stopped")).toBeInTheDocument();
    expect(within(docs).queryByTestId("frontend-verdict")).toBeNull();
  });

  it("names the environment on each row only across all environments", async () => {
    renderOverview({ env: "all" });
    await ready();
    expect(within(screen.getByTestId("frontend-prod-web")).getByText("prod")).toBeInTheDocument();
    expect(screen.getByTestId("frontend-dev-web")).toBeInTheDocument();
    expect(screen.getByTestId("frontend-list")).toBeInTheDocument();
  });

  it("copies the address", async () => {
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    renderOverview();
    await ready();
    await user.click(within(screen.getByTestId("frontend-prod-web")).getAllByTestId("frontend-copy")[0]!);
    expect(writeText).toHaveBeenCalledWith("https://app.reliantlabs.io");
    await waitFor(() => expect(screen.getByRole("button", { name: /Copied https:\/\/app\.reliantlabs/ })).toBeInTheDocument());
  });

  it("leaves the domain column out when the org's domains could not be read, and still shows the address", async () => {
    renderOverview({ domains: { availability: "unreachable", domains: [], detail: "" } });
    await ready();
    const web = screen.getByTestId("frontend-prod-web");
    expect(within(web).getByTestId("frontend-url")).toBeInTheDocument();
    expect(within(web).queryByTestId("frontend-domains")).toBeNull();
  });

  it("explains how to ship a frontend when the scope has none", async () => {
    const withoutSites = new Map([...FIXTURE_HOSTED].map(([env, workloads]) => [env, new Map([...workloads].filter(([, w]) => w.tier !== "static"))]));
    renderOverview({ hosted: withoutSites });
    await ready();
    const empty = screen.getByTestId("frontends-empty");
    expect(empty).toHaveTextContent("No frontend deployed here yet");
    expect(within(empty).getAllByTestId("getting-started-command").map((c) => c.textContent)).toEqual(
      expect.arrayContaining([expect.stringContaining("forge env deploy prod")])
    );
  });

  it("shows a skeleton, not the empty state, while the hosted status is still loading", async () => {
    renderOverview({ hosted: new Map(), hostedLoading: true });
    await ready();
    expect(screen.getByTestId("frontends-loading")).toBeInTheDocument();
    expect(screen.queryByTestId("frontends-empty")).toBeNull();
  });

  it("does not link to APIs: an API is not a destination for a person", async () => {
    renderOverview({ env: "all" });
    await ready();
    expect(screen.queryByTestId("overview-live-links")).toBeNull();
    const hrefs = screen.getAllByRole("link").map((a) => a.getAttribute("href"));
    expect(hrefs).not.toContain("https://api.reliantlabs.io");
    expect(hrefs).not.toContain("https://admin.reliantlabs.io");
  });
});
