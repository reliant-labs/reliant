// Copyright (c) 2025 Reliant Labs

/**
 * Observability › Dashboards against the in-memory DashboardService: the
 * landing (starred chips, a card per hub or provided dashboard, the team's
 * own as rows), search across everything, the control-plane shape of sixteen
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
    // The built-in is a card of its own.
    const costs = await screen.findByTestId("dashboards-hub-org-costs");
    expect(within(costs).getByRole("link", { name: /Organization costs/ })).toHaveAttribute("href", "/d/builtin/org-costs");
    expect(within(costs).getByText("Requires cost-read permission")).toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-empty")).not.toBeInTheDocument();
  });

  it("leads with what forge provides as a few cards, the team's own as rows, for the scoped environment", async () => {
    const { onCall } = renderList();
    const starred = await screen.findByTestId("dashboards-starred");
    expect(onCall).toHaveBeenCalledWith("ListDashboards", expect.objectContaining({ projectId: FIXTURE_PROJECT, envId: "env-prod" }));
    // Inside the project, "shop: " says nothing: it is dropped.
    expect(within(starred).getAllByRole("link").map((a) => a.textContent)).toEqual(["Overview", "Frontend · web", "Checkout latency"]);
    // Stars are one row of chips: no panel previews, so the page runs no panel query.
    expect(onCall).not.toHaveBeenCalledWith("QueryPanel", expect.anything());

    const cards = within(screen.getByTestId("dashboards-hubs")).getAllByRole("listitem").map((li) => li.dataset.testid);
    expect(cards).toEqual(["dashboards-hub-overview", "dashboards-hub-services", "dashboards-hub-releases", "dashboards-hub-workloads", "dashboards-hub-org-llm"]);
    // Every service's dashboard (and the jobs one) is one card, opening the first.
    const services = screen.getByTestId("dashboards-hub-services");
    expect(within(services).getByRole("link", { name: "Services" })).toHaveAttribute("href", "/d/release/service-api");
    expect(within(screen.getByTestId("dashboards-hub-services-tabs"))).toBeTruthy();
    expect(screen.getByTestId("dashboards-hub-services-tabs")).toHaveTextContent("api · orders · Jobs");
    expect(within(screen.getByTestId("dashboards-hub-org-llm")).queryByText("Requires cost-read permission")).not.toBeInTheDocument();

    const own = screen.getByTestId("dashboards-own");
    expect(within(own).getAllByRole("link").map((a) => a.textContent)).toEqual(["Checkout funnel", "Frontend · web", "Checkout latency", "Daemon fleet", "Billing funnel"]);
    expect(screen.getByTestId("dashboards-own-count")).toHaveTextContent("5");
    // Where a row came from is said quietly; the release and the file are its tooltip.
    const checkout = within(own).getByText("Checkout funnel").closest("li")!;
    expect(within(checkout).getByTestId("dashboards-row-source")).toHaveTextContent("repo");
    expect(within(checkout).getByTestId("dashboards-row-source").closest("[aria-describedby]")).toHaveAccessibleDescription(/Shipped in v1\.8\.2.*deploy\/observability\/dashboards\/checkout\.json/);
  });

  it("finds one service's dashboard by search, and narrows by group, source and owner", async () => {
    const user = userEvent.setup();
    renderList();
    await screen.findByTestId("dashboards-hubs");
    await user.type(screen.getByTestId("dashboards-search"), "orders");
    const results = () => within(screen.getByTestId("dashboards-results")).getAllByTestId("dashboards-row").map((row) => row.querySelector("a")?.getAttribute("aria-label"));
    expect(results()).toContain("shop: orders service");
    expect(screen.queryByTestId("dashboards-hubs")).not.toBeInTheDocument();

    await user.clear(screen.getByTestId("dashboards-search"));
    await user.type(screen.getByTestId("dashboards-search"), "daemons");
    expect(results()).toEqual(["Daemon fleet"]);
    await user.click(screen.getByTestId("dashboards-chip-release"));
    expect(screen.getByTestId("dashboards-no-match")).toHaveTextContent("No dashboards match");
    await user.click(screen.getByTestId("dashboards-clear"));
    await user.click(screen.getByTestId("dashboards-chip-group"));
    await user.click(screen.getByRole("menuitemradio", { name: "Jobs" }));
    expect(results()).toEqual(["shop: jobs"]);
    await user.click(screen.getByTestId("dashboards-clear"));
    await user.click(screen.getByTestId("dashboards-chip-owner"));
    await user.click(screen.getByRole("menuitemradio", { name: "repo" }));
    expect(results()).toEqual(["Checkout funnel", "Frontend · web"]);
  });

  it("stays a handful of cards over control-plane's sixteen services and hosted built-ins", async () => {
    renderList({ dashboardsManyServices: true });
    const hubs = await screen.findByTestId("dashboards-hubs");
    expect(within(hubs).getAllByRole("listitem").map((li) => li.dataset.testid)).toEqual([
      "dashboards-hub-overview",
      "dashboards-hub-product",
      "dashboards-hub-services",
      "dashboards-hub-releases",
      "dashboards-hub-workloads",
      "dashboards-hub-org-llm",
    ]);
    expect(within(screen.getByTestId("dashboards-hub-services")).getByLabelText("16 services")).toBeInTheDocument();
    expect(screen.getByTestId("dashboards-hub-product-tabs")).toHaveTextContent("Users · Product · Acquisition");
    // The built-in Product usage is what forge's Users, Product and Acquisition say: it is not listed twice.
    expect(screen.queryAllByRole("link").map((a) => a.getAttribute("href"))).not.toContain("/d/builtin/usage");
    expect(within(screen.getByTestId("dashboards-own")).getAllByTestId("dashboards-row")).toHaveLength(1);
  });

  it("stars and unstars at once, for the caller, and pins the star at the top", async () => {
    const user = userEvent.setup();
    const { onCall } = renderList();
    const workloads = await screen.findByTestId("dashboards-hub-workloads");
    await user.click(within(workloads).getByRole("button", { name: "Star shop: workloads" }));
    expect(within(workloads).getByRole("button", { name: "Unstar shop: workloads" })).toHaveAttribute("aria-pressed", "true");
    expect(onCall).toHaveBeenCalledWith("StarDashboard", expect.objectContaining({ ref: expect.objectContaining({ kind: "release", key: "workloads" }) }));
    await waitFor(() => expect(within(screen.getByTestId("dashboards-starred")).getByText("Workloads")).toBeInTheDocument());
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

  it("works for a young app with two dashboards: their cards, and nothing of the team's yet", async () => {
    renderList({ dashboardsFew: true });
    await screen.findByTestId("dashboards-list");
    expect(within(screen.getByTestId("dashboards-hubs")).getAllByRole("listitem").map((li) => li.dataset.testid)).toEqual(["dashboards-hub-overview", "dashboards-hub-services", "dashboards-hub-org-llm"]);
    expect(screen.getByTestId("dashboards-own-empty")).toHaveTextContent("None yet");
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
