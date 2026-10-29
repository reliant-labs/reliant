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
import { Code, ConnectError } from "@connectrpc/connect";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT, type FixtureOptions } from "../../__fixtures__/fixtureApi";
import { renderWithApi } from "../../__tests__/obsTestUtils";
import { DashboardsList, type DashboardsSearch } from "../DashboardsList";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

const hrefs = {
  dashboard: (ref: DashboardRef) => `/d/${ref.kind}/${ref.key}`,
  hub: {
    alerts: "/alerts",
    releases: "/releases",
    errors: "/errors",
    release: (version: string) => `/releases/${version}`,
    workload: (name: string, env: string | null) => `/workloads/${env ?? "all"}/${name}`,
    logs: (service: string) => `/logs?service=${service}`,
  },
};

type Api = ReturnType<typeof createFixtureApi>;

function renderList(options: FixtureOptions = {}, env = "prod", initial: DashboardsSearch = { view: "all" }, patch?: (api: Api) => void) {
  const onCall = vi.fn();
  const navigateTo = vi.fn();
  const api = createFixtureApi({ ...options, onCall });
  patch?.(api);
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
    // The Overview lands first; with nothing deployed it says so, and the built-in sits in Platform.
    expect(await screen.findByTestId("hub-health-undeployed")).toHaveTextContent("after the first deploy");
    const platform = screen.getByTestId("hub-group-platform");
    expect(within(platform).getByRole("link", { name: "Organization costs" })).toHaveAttribute("href", "/d/builtin/org-costs");
    expect(screen.queryByTestId("dashboards-empty")).not.toBeInTheDocument();
  });

  it("groups the project's dashboards for the scoped environment, in page order, with counts", async () => {
    const { onCall } = renderList();
    const starred = await screen.findByTestId("dashboards-starred");
    expect(onCall).toHaveBeenCalledWith("ListDashboards", expect.objectContaining({ projectId: FIXTURE_PROJECT, envId: "env-prod" }));
    expect(within(starred).getAllByRole("link").map((a) => a.textContent)).toEqual(["shop: overview", "Frontend · web", "Checkout latency"]);
    // A starred card draws its headline panels through QueryPanel.
    await waitFor(() => expect(onCall).toHaveBeenCalledWith("QueryPanel", expect.objectContaining({ panel: expect.objectContaining({ id: "p95" }) })));

    const sections = screen.getAllByTestId(/^dashboards-group-[a-z]+$/).map((el) => el.dataset.testid!.replace("dashboards-group-", ""));
    expect(sections).toEqual(["overview", "services", "jobs", "product", "platform", "releases", "custom"]);
    const titles = (group: string) => within(screen.getByTestId(`dashboards-group-${group}`)).getAllByRole("row").slice(1).map((r) => r.querySelector("a")?.getAttribute("aria-label"));
    expect(titles("services")).toEqual(["shop: orders service", "shop: api service"]);
    expect(titles("jobs")).toEqual(["shop: jobs"]);
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
    expect(screen.queryByRole("table", { name: "Services dashboards" })).not.toBeInTheDocument();
    await user.click(toggle);
    expect(screen.getByRole("table", { name: "Services dashboards" })).toBeInTheDocument();
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
    expect(within(empty).getByText("forge obs dashboards add-panel <slug> --panel <panel.json>")).toBeInTheDocument();
    expect(within(empty).getByTestId("dashboards-getting-started-docs")).toHaveAttribute("href", expect.stringContaining("#dashboards"));
    expect(await screen.findByTestId("dashboards-templates")).toBeInTheDocument();
  });

  it("says when the dashboards cannot be read", async () => {
    renderList({ dashboardsFailWith: Code.Unavailable });
    // Unavailable is retried once before it is shown.
    expect(await screen.findByTestId("obs-error-unavailable", {}, { timeout: 5000 })).toBeInTheDocument();
  });
});

describe("Dashboards › Overview hub", () => {
  it("lands on the Overview: health, failing jobs, releases, issues and a card per group", async () => {
    const { onCall } = renderList({}, "prod", {});
    expect(await screen.findByTestId("dashboards-hub")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByTestId("dashboards")).toHaveAttribute("data-view", "overview");
    // The project Overview's strip and process table, over the same reads.
    expect(screen.getByTestId("observability-strip")).toBeInTheDocument();
    await waitFor(() => expect(onCall).toHaveBeenCalledWith("GetServiceOverview", expect.objectContaining({ projectId: FIXTURE_PROJECT, envIds: ["env-prod"] })));
    // Only the job whose latest run failed: nightly-report failed, then recovered.
    const jobs = await screen.findByTestId("hub-jobs-failing");
    expect(within(jobs).getAllByRole("link").map((a) => [a.textContent, a.getAttribute("href")])).toEqual([["search-reindex", "/workloads/prod/search-reindex"]]);
    expect(within(screen.getByTestId("hub-jobs")).getByRole("link", { name: "Open shop: jobs" })).toHaveAttribute("href", "/d/release/jobs");
    expect(await within(screen.getByTestId("hub-releases")).findByTestId("hub-releases-list")).toBeInTheDocument();
    expect(await screen.findByTestId("hub-issues-count")).toBeInTheDocument();
    expect(within(screen.getByTestId("hub-issues")).getByRole("link", { name: "Open the inbox" })).toHaveAttribute("href", "/errors");
    // Every group has a card, with its count and its first dashboards.
    expect(screen.getByTestId("hub-group-services-count")).toHaveTextContent("2");
    expect(within(screen.getByTestId("hub-group-overview")).getByRole("link", { name: "shop: overview" })).toHaveAttribute("href", "/d/release/overview");
    expect(within(screen.getByTestId("hub-starred")).getAllByRole("link").map((a) => a.textContent)).toEqual(["shop: overview", "Frontend · web", "Checkout latency"]);
    expect(within(screen.getByTestId("hub-recent")).getAllByRole("link")[0]).toHaveTextContent("Checkout latency");
  });

  it("opens the list narrowed to a group from its card, and back to the Overview from the tab", async () => {
    const user = userEvent.setup();
    renderList({}, "prod", {});
    await user.click(await screen.findByTestId("hub-group-services-open"));
    expect(screen.getByRole("tab", { name: /All dashboards/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getAllByTestId(/^dashboards-group-[a-z]+$/).map((el) => el.dataset.testid)).toEqual(["dashboards-group-services"]);
    expect(screen.getByTestId("dashboards-chip-group")).toHaveTextContent("Group: Services");
    await user.click(screen.getByRole("tab", { name: "Overview" }));
    expect(await screen.findByTestId("dashboards-hub")).toBeInTheDocument();
  });

  it("opens the whole list from its card, and the template gallery from New dashboard", async () => {
    // jsdom does not lay out, so it has no scrollIntoView.
    const scrolled = vi.fn();
    Element.prototype.scrollIntoView = scrolled;
    const user = userEvent.setup();
    renderList({}, "prod", {});
    await screen.findByTestId("dashboards-hub");
    expect(screen.queryByTestId("dashboards-templates")).not.toBeInTheDocument();
    await user.click(screen.getByTestId("hub-open-all"));
    expect(screen.getAllByTestId(/^dashboards-group-[a-z]+$/)).toHaveLength(7);
    await user.click(screen.getByRole("tab", { name: "Overview" }));
    await user.click(await screen.findByTestId("dashboards-new"));
    await user.click(screen.getByTestId("dashboards-new-template"));
    expect(await screen.findByTestId("dashboards-templates")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /All dashboards/ })).toHaveAttribute("aria-selected", "true");
    expect(scrolled).toHaveBeenCalledWith({ block: "start", behavior: "smooth" });
  });

  it("opens the list when a search is typed", async () => {
    renderList({}, "prod", { q: "jobs" });
    expect(await screen.findByTestId("dashboards-group-jobs")).toBeInTheDocument();
    expect(screen.queryByTestId("dashboards-hub")).not.toBeInTheDocument();
  });

  it("works for a young app with two dashboards", async () => {
    renderList({ dashboardsFew: true }, "prod", {});
    await screen.findByTestId("dashboards-hub");
    expect(screen.getByTestId("hub-group-overview-count")).toHaveTextContent("1");
    expect(screen.getByTestId("hub-group-services-count")).toHaveTextContent("1");
    expect(within(screen.getByTestId("hub-group-jobs")).getByText("None yet")).toBeInTheDocument();
    expect(screen.queryByTestId("hub-group-jobs-open")).not.toBeInTheDocument();
    // No jobs dashboard to open: the card still lists the failing job, with nowhere else to send the reader.
    expect(await screen.findByTestId("hub-jobs-failing")).toBeInTheDocument();
    expect(within(screen.getByTestId("hub-jobs")).queryByRole("link", { name: /^Open/ })).not.toBeInTheDocument();
    expect(screen.queryByTestId("hub-recent")).not.toBeInTheDocument();
  });

  it("keeps the health on an app with no dashboards, and explains how to get some", async () => {
    renderList({ dashboardsEmpty: true }, "prod", {});
    await screen.findByTestId("dashboards-hub");
    expect(screen.getByTestId("observability-strip")).toBeInTheDocument();
    expect(screen.getByTestId("dashboards-empty")).toHaveTextContent("Dashboards ship with your releases");
    expect(screen.queryByTestId("hub-groups")).not.toBeInTheDocument();
  });

  it("says when a source cannot answer, instead of showing a zero", async () => {
    renderList({}, "prod", {}, (api) => {
      api.listIssues = vi.fn().mockRejectedValue(new ConnectError("no", Code.PermissionDenied));
      api.listWorkloads = vi.fn().mockRejectedValue(new ConnectError("not set up", Code.FailedPrecondition));
    });
    await screen.findByTestId("dashboards-hub");
    expect(await within(screen.getByTestId("hub-issues")).findByTestId("hub-unavailable")).toHaveTextContent("You do not have access to issues here.");
    expect(await within(screen.getByTestId("hub-jobs")).findByTestId("hub-unavailable")).toHaveTextContent("Job runs need observability set up for this app.");
    expect(screen.queryByTestId("hub-issues-count")).not.toBeInTheDocument();
  });

  it("says an unrecorded environment has no health, rather than asking for it", async () => {
    const { onCall } = renderList({}, "nowhere", {});
    expect(await screen.findByTestId("hub-health-unrecorded")).toHaveTextContent("recorded no deploy of nowhere");
    expect(onCall).not.toHaveBeenCalledWith("GetServiceOverview", expect.anything());
  });
});
