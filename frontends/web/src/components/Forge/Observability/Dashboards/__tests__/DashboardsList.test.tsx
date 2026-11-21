// Copyright (c) 2025 Reliant Labs

/**
 * Observability › Dashboards against the in-memory DashboardService: the
 * gallery (a card per hub or dashboard, stars first, each with its source
 * badge and preview), search and the source filter, the control-plane shape of sixteen
 * services — the filters, starring, the template
 * gallery and New dashboard — each write naming its environment — and the
 * read-only, empty, unavailable and failed states.
 */

import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code } from "@connectrpc/connect";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, type FixtureOptions } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { DashboardsList, type DashboardsSearch } from "../DashboardsList";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const hrefs = { dashboard: (ref: DashboardRef) => `/d/${ref.kind}/${ref.key}` };

function renderList(options: FixtureOptions = {}, env = "prod", initial: DashboardsSearch = {}) {
  const onCall = vi.fn();
  const navigateTo = vi.fn();
  const api = createFixtureApi({ ...options, onCall });
  function Stateful() {
    const [search, setSearch] = useState<DashboardsSearch>(initial);
    return (
      <DashboardsList
        project={FIXTURE_PROJECT}
        env={env}
        environments={FIXTURE_ENVS}
        search={search}
        onSearch={(next) => setSearch((prev) => ({ ...prev, ...next }))}
        hrefs={hrefs}
        navigateTo={navigateTo}
      />
    );
  }
  renderWithApi(api, <Stateful />);
  return { onCall, navigateTo };
}

describe("Dashboards", () => {
  it("discovers organization accounting without any deployed environments or authoring rights", async () => {
    const api = createFixtureApi();
    api.listDashboards = vi.fn().mockResolvedValue({ canWrite: false, dashboards: [{
      ref: { kind: "builtin", projectId: "", key: "org-costs" }, source: "builtin", viewKind: "organization-costs",
      title: "Organization costs", description: "Canonical current-period accounting", readOnly: true, starred: false,
      lastViewedAtMs: null, panelCount: 0, previewPanels: [], release: null, workspace: null, category: "organization",
    }] });
    renderWithApi(api, <DashboardsList project={FIXTURE_PROJECT} env="all" environments={[]} search={{}} onSearch={vi.fn()} hrefs={hrefs} navigateTo={vi.fn()} />);
    // The built-in is a card of its own, badged, saying what opening it needs and that it has no chart to preview.
    const costs = await screen.findByTestId("dashboards-card-org-costs");
    expect(within(costs).getByRole("link", { name: /Organization costs/ })).toHaveAttribute("href", "/d/builtin/org-costs");
    expect(within(costs).getByText("Requires cost-read permission")).toBeInTheDocument();
    expect(screen.getByTestId("dashboards-card-org-costs-badge")).toHaveTextContent("Built-in");
    expect(screen.getByTestId("dashboards-card-org-costs-preview")).toHaveTextContent("No chart to preview");
    expect(screen.queryByTestId("dashboards-empty")).not.toBeInTheDocument();
  });

  it("lays every dashboard out as a card, stars first, forge's hubs one card each, for the scoped environment", async () => {
    const { onCall } = renderList();
    const gallery = await screen.findByTestId("dashboards-gallery");
    expect(onCall).toHaveBeenCalledWith("ListDashboards", expect.objectContaining({ projectId: FIXTURE_PROJECT, envId: "env-prod" }));
    const titles = within(gallery).getAllByRole("link").map((a) => a.textContent);
    // Inside the project, "shop: " says nothing: it is dropped. The caller's stars lead.
    expect(new Set(titles.slice(0, 3))).toEqual(new Set(["Overview", "Frontend · web", "Checkout latency"]));
    // Every service's dashboard (and the jobs one) is one card, opening the first.
    const services = screen.getByTestId("dashboards-card-services");
    expect(within(services).getByRole("link", { name: "Services" })).toHaveAttribute("href", "/d/release/service-api");
    expect(screen.getByTestId("dashboards-card-services-tabs")).toHaveAttribute("title", "api · orders · Jobs");
    // Each card says where it came from.
    expect(screen.getByTestId("dashboards-card-checkout-badge")).toHaveTextContent("From repo");
    expect(within(screen.getByTestId("dashboards-card-org-llm")).queryByText("Requires cost-read permission")).not.toBeInTheDocument();
    // And the gallery ends on a way to make one.
    expect(screen.getByTestId("dashboards-gallery-new")).toHaveTextContent("New dashboard");
  });

  it("finds one service's dashboard by search, and narrows by source", async () => {
    const user = userEvent.setup();
    renderList();
    await screen.findByTestId("dashboards-gallery");
    await user.type(screen.getByTestId("dashboards-search"), "orders");
    // A search lists every match as its own card, the ones inside a hub included.
    expect(screen.queryByTestId("dashboards-card-services")).not.toBeInTheDocument();
    expect(within(screen.getByTestId("dashboards-gallery")).getByRole("link", { name: /orders service/i })).toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-gallery-new")).not.toBeInTheDocument();

    await user.clear(screen.getByTestId("dashboards-search"));
    await user.type(screen.getByTestId("dashboards-search"), "daemons");
    expect(within(screen.getByTestId("dashboards-gallery")).getAllByRole("link").map((a) => a.textContent)).toEqual(["Daemon fleet"]);
    // Each filter counts what it would show for the search.
    expect(screen.getByTestId("dashboards-source-all")).toHaveTextContent("1");
    await user.click(screen.getByTestId("dashboards-source-builtin"));
    expect(screen.getByTestId("dashboards-source-builtin")).toHaveAttribute("aria-checked", "true");
    expect(screen.getByTestId("dashboards-no-match")).toHaveTextContent("No dashboards match");
    await user.click(screen.getByTestId("dashboards-source-all"));
    expect(screen.queryByTestId("dashboards-no-match")).not.toBeInTheDocument();
  });

  it("stays a handful of cards over control-plane's sixteen services and hosted built-ins", async () => {
    renderList({ dashboardsManyServices: true });
    await screen.findByTestId("dashboards-gallery");
    expect(screen.getByTestId("dashboards-card-services")).toBeInTheDocument();
    expect(screen.getByTestId("dashboards-card-product-tabs")).toHaveAttribute("title", expect.stringContaining("Users · Product · Acquisition"));
    expect(screen.getAllByTestId(/^dashboards-card-[^-]+(-[^-]+)*$/).filter((el) => el.tagName === "LI").length).toBeLessThan(10);
    // The built-in Product usage is what forge's Users, Product and Acquisition say: it is not listed twice.
    expect(screen.queryAllByRole("link").map((a) => a.getAttribute("href"))).not.toContain("/d/builtin/usage");
  });

  it("stars and unstars at once, for the caller, and moves the star to the front", async () => {
    const user = userEvent.setup();
    const { onCall } = renderList();
    const workloads = await screen.findByTestId("dashboards-card-workloads");
    await user.click(within(workloads).getByRole("button", { name: "Star Workloads" }));
    expect(within(screen.getByTestId("dashboards-card-workloads")).getByRole("button", { name: "Unstar Workloads" })).toHaveAttribute("aria-pressed", "true");
    expect(onCall).toHaveBeenCalledWith("StarDashboard", expect.objectContaining({ ref: expect.objectContaining({ kind: "release", key: "workloads" }) }));
    await waitFor(() => {
      const titles = within(screen.getByTestId("dashboards-gallery")).getAllByRole("link").map((a) => a.textContent);
      expect(titles.indexOf("Workloads")).toBeLessThan(4);
    });
  });

  it("starts from a template: the copy names its environment, and opens when saved", async () => {
    const user = userEvent.setup();
    const { onCall, navigateTo } = renderList();
    await user.click(await screen.findByTestId("dashboards-template-service-red"));
    const confirm = screen.getByTestId("dashboard-copy-confirm");
    expect(confirm).toHaveTextContent("Save copy for prod");
    await user.click(screen.getByRole("radio", { name: /All environments/ }));
    expect(confirm).toHaveTextContent("Save copy for all environments");
    await user.click(confirm);
    await waitFor(() => expect(navigateTo).toHaveBeenCalledWith("/d/workspace/service-red"));
    expect(onCall).toHaveBeenCalledWith("SaveAsCopy", expect.objectContaining({ from: expect.objectContaining({ kind: "template", key: "service-red" }), envScope: { envIds: [] } }));
  });

  it("creates a blank dashboard scoped to the environment, and says so when the name is taken", async () => {
    const user = userEvent.setup();
    const { onCall, navigateTo } = renderList();
    await user.click(await screen.findByTestId("dashboards-new"));
    await user.click(screen.getByTestId("dashboards-new-blank"));
    await user.type(screen.getByTestId("dashboard-new-title"), "Checkout latency");
    expect(screen.getByTestId("dashboard-new-slug")).toHaveValue("checkout-latency");
    await user.click(screen.getByTestId("dashboard-create-confirm"));
    expect(await screen.findByTestId("dashboard-write-error")).toHaveTextContent("already exists");
    await user.clear(screen.getByTestId("dashboard-new-slug"));
    await user.type(screen.getByTestId("dashboard-new-slug"), "checkout-latency-2");
    await user.click(screen.getByTestId("dashboard-create-confirm"));
    await waitFor(() => expect(navigateTo).toHaveBeenCalledWith("/d/workspace/checkout-latency-2"));
    expect(onCall).toHaveBeenCalledWith("SaveDashboard", expect.objectContaining({ slug: "checkout-latency-2", baseVersion: 0, envScope: { envIds: ["env-prod"] } }));
  });

  it("keeps a member who is not an admin to reading and starring", async () => {
    renderList({ dashboardsReadOnly: true });
    expect(await screen.findByTestId("dashboards-read-only")).toHaveTextContent("needs an organization admin");
    expect(screen.getByTestId("dashboards-template-blank")).toBeDisabled();
  });

  it("explains an empty project, and a failure", async () => {
    renderList({ dashboardsEmpty: true });
    const empty = await screen.findByTestId("dashboards-empty");
    expect(empty).toHaveTextContent("Dashboards ship with your releases");
    expect(within(empty).getByTestId("dashboards-getting-started-about")).toHaveTextContent("the dashboards forge generates");
    expect(within(empty).getByText("reliant forge obs dashboards add-panel <slug> --panel <panel.json>")).toBeInTheDocument();
    expect(within(empty).getByTestId("dashboards-getting-started-docs")).toHaveAttribute("href", expect.stringContaining("#dashboards"));
    expect(await screen.findByTestId("dashboards-templates")).toBeInTheDocument();
  });

  it("says when the dashboards cannot be read", async () => {
    renderList({ dashboardsFailWith: Code.Unavailable });
    // Unavailable is retried once before it is shown.
    expect(await screen.findByTestId("obs-error-unavailable", {}, { timeout: 5000 })).toBeInTheDocument();
  });
});

describe("Dashboards is the dashboard browser, not a second Overview", () => {
  it("lands on the list, with no tabs and no health panels", async () => {
    const { onCall } = renderList();
    expect(await screen.findByTestId("dashboards-list")).toBeInTheDocument();
    expect(screen.queryByRole("tab")).not.toBeInTheDocument();
    expect(screen.queryByTestId("observability-strip")).not.toBeInTheDocument();
    // Health is the project Overview's: this page asks for none of it.
    for (const rpc of ["GetServiceOverview", "ListWorkloads", "ListIssues", "ListReleases"]) {
      expect(onCall).not.toHaveBeenCalledWith(rpc, expect.anything());
    }
  });

  it("works for a young app with two dashboards: their cards, and a way to make one", async () => {
    renderList({ dashboardsFew: true });
    await screen.findByTestId("dashboards-list");
    for (const id of ["overview", "services", "org-llm"]) expect(screen.getByTestId(`dashboards-card-${id}`)).toBeInTheDocument();
    expect(screen.getByTestId("dashboards-gallery-new")).toBeInTheDocument();
  });

  it("opens the template gallery from New dashboard", async () => {
    // jsdom does not lay out, so it has no scrollIntoView.
    const scrolled = vi.fn();
    Element.prototype.scrollIntoView = scrolled;
    const user = userEvent.setup();
    renderList();
    await user.click(await screen.findByTestId("dashboards-new"));
    await user.click(screen.getByTestId("dashboards-new-template"));
    expect(screen.getByTestId("dashboards-templates")).toBeInTheDocument();
    expect(scrolled).toHaveBeenCalledWith({ block: "start", behavior: "smooth" });
  });
});
