// Copyright (c) 2025 Reliant Labs

/**
 * Observability › Dashboards against the in-memory DashboardService: the
 * Overview hub (the landing view) and the grouped list — starred pinned with
 * previews, collapsible groups with counts, search across groups, the
 * fallback grouping of an older server — the filters, starring, the template
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
    // The built-in is its own first section, not repeated in Platform.
    const builtins = await screen.findByTestId("dashboards-group-builtin");
    expect(within(builtins).getByRole("link", { name: /Organization costs/ })).toHaveAttribute("href", "/d/builtin/org-costs");
    expect(within(builtins).getByText("Requires cost-read permission")).toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-group-platform")).not.toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-empty")).not.toBeInTheDocument();
  });

  it("groups the project's dashboards for the scoped environment, in page order, with counts", async () => {
    const { onCall } = renderList();
    const starred = await screen.findByTestId("dashboards-starred");
    expect(onCall).toHaveBeenCalledWith("ListDashboards", expect.objectContaining({ projectId: FIXTURE_PROJECT, envId: "env-prod" }));
    expect(within(starred).getAllByRole("link").map((a) => a.textContent)).toEqual(["shop: overview", "Frontend · web", "Checkout latency"]);
    // Stars are one row of chips: no panel previews, so the page runs no panel query.
    expect(onCall).not.toHaveBeenCalledWith("QueryPanel", expect.anything());

    const sections = screen.getAllByTestId(/^dashboards-group-[a-z]+$/).map((el) => el.dataset.testid!.replace("dashboards-group-", ""));
    expect(sections).toEqual(["builtin", "overview", "services", "jobs", "product", "platform", "releases", "custom"]);
    const titles = (group: string) => within(screen.getByTestId(`dashboards-group-${group}`)).getAllByRole("row").slice(1).map((r) => r.querySelector("a")?.getAttribute("aria-label"));
    expect(titles("services")).toEqual(["shop: orders service", "shop: api service"]);
    expect(titles("jobs")).toEqual(["shop: jobs"]);
    // The client's own LLM usage built-in leads, in Built-in; not repeated in Product.
    expect(titles("builtin")).toEqual(["LLM usage"]);
    expect(titles("product")).toEqual(["Checkout funnel", "Frontend · web", "Billing funnel"]);
    expect(titles("platform")).toEqual(["shop: workloads", "Daemon fleet"]);
    expect(screen.getByTestId("dashboards-group-product-count")).toHaveTextContent("3");

    // A row names its Forge workload; where it came from is secondary, the release in its tooltip.
    const services = screen.getByTestId("dashboards-group-services");
    expect(within(services).getAllByTestId("dashboards-row-workload").map((el) => el.textContent)).toEqual(["Workloadorders", "Workloadapi"]);
    expect(within(services).queryByRole("link", { name: "v1.8.3" })).not.toBeInTheDocument();
    const checkout = within(screen.getByTestId("dashboards-group-product")).getByText("Checkout funnel").closest("tr")!;
    expect(within(checkout).getByTestId("dashboards-row-source")).toHaveTextContent("repo");
    // The tooltip describes its trigger: the wrapper Tooltip makes focusable around the word.
    expect(within(checkout).getByTestId("dashboards-row-source").closest("[aria-describedby]")).toHaveAccessibleDescription(/Shipped in v1\.8\.2.*deploy\/observability\/dashboards\/checkout\.json/);
  });

  it("folds and unfolds a group", async () => {
    const user = userEvent.setup();
    renderList();
    const toggle = await screen.findByTestId("dashboards-group-services-toggle");
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    const services = screen.getByTestId("dashboards-group-services");
    expect(within(services).queryByRole("link")).not.toBeInTheDocument();
    await user.click(toggle);
    expect(within(services).getAllByRole("link")).toHaveLength(2);
  });

  it("searches across every group, unfolding what it finds, and narrows by group, source and owner", async () => {
    const user = userEvent.setup();
    renderList();
    await user.click(await screen.findByTestId("dashboards-group-product-toggle"));
    await user.type(screen.getByTestId("dashboards-search"), "billing");
    // The folded group opens for the search, and the groups without a match go.
    expect(screen.getByTestId("dashboards-group-product-toggle")).toHaveAttribute("aria-expanded", "true");
    expect(screen.getAllByTestId(/^dashboards-group-[a-z]+$/).map((el) => el.dataset.testid)).toEqual(["dashboards-group-product"]);
    expect(screen.getByTestId("dashboards-group-product-count")).toHaveTextContent("1");

    await user.clear(screen.getByTestId("dashboards-search"));
    await user.type(screen.getByTestId("dashboards-search"), "daemons");
    expect(within(screen.getByTestId("dashboards-group-platform")).getAllByRole("row")).toHaveLength(2);
    await user.click(screen.getByTestId("dashboards-chip-release"));
    expect(screen.getByTestId("dashboards-no-match")).toHaveTextContent("No dashboards match");
    await user.click(screen.getByTestId("dashboards-clear"));
    await user.click(screen.getByTestId("dashboards-chip-group"));
    await user.click(screen.getByRole("menuitemradio", { name: "Jobs" }));
    expect(screen.getAllByTestId(/^dashboards-group-[a-z]+$/).map((el) => el.dataset.testid)).toEqual(["dashboards-group-jobs"]);
    await user.click(screen.getByTestId("dashboards-clear"));
    await user.click(screen.getByTestId("dashboards-chip-owner"));
    await user.click(screen.getByRole("menuitemradio", { name: "repo" }));
    expect(screen.getAllByTestId(/^dashboards-group-[a-z]+$/).map((el) => el.dataset.testid)).toEqual(["dashboards-group-product"]);
  });

  it("groups an older server's dashboards by key and source", async () => {
    renderList({ dashboardsUngrouped: true });
    await screen.findByTestId("dashboards-starred");
    const titles = (group: string) => within(screen.getByTestId(`dashboards-group-${group}`)).getAllByRole("row").slice(1).map((r) => r.querySelector("a")?.getAttribute("aria-label"));
    expect(titles("overview")).toEqual(["shop: overview"]);
    expect(titles("services")).toEqual(["shop: orders service", "shop: api service"]);
    expect(titles("jobs")).toEqual(["shop: jobs"]);
    expect(titles("platform")).toEqual(["shop: workloads"]);
    expect(titles("custom")).toEqual(["Checkout funnel", "Frontend · web", "Checkout latency", "Daemon fleet", "Billing funnel"]);
    // The client's own built-in is in Built-in, whatever the server says.
    expect(titles("builtin")).toEqual(["LLM usage"]);
    expect(screen.queryByTestId("dashboards-group-product")).not.toBeInTheDocument();
  });

  it("stars and unstars at once, for the caller, and pins the star at the top", async () => {
    const user = userEvent.setup();
    const { onCall } = renderList();
    const platform = await screen.findByTestId("dashboards-group-platform");
    await user.click(within(platform).getByRole("button", { name: "Star shop: workloads" }));
    expect(within(platform).getByRole("button", { name: "Unstar shop: workloads" })).toHaveAttribute("aria-pressed", "true");
    expect(onCall).toHaveBeenCalledWith("StarDashboard", expect.objectContaining({ ref: expect.objectContaining({ kind: "release", key: "workloads" }) }));
    await waitFor(() => expect(within(screen.getByTestId("dashboards-starred")).getByText("shop: workloads")).toBeInTheDocument());
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

  it("works for a young app with two dashboards: no section for an empty group", async () => {
    renderList({ dashboardsFew: true });
    await screen.findByTestId("dashboards-list");
    expect(screen.getAllByTestId(/^dashboards-group-[a-z]+$/).map((el) => el.dataset.testid)).toEqual(["dashboards-group-builtin", "dashboards-group-overview", "dashboards-group-services"]);
    expect(screen.queryByText("None yet")).not.toBeInTheDocument();
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
