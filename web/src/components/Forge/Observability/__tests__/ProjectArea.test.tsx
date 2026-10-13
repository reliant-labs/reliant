// Copyright (c) 2025 Reliant Labs

/**
 * The project-level Observability area (owner decisions 1 and 2): environment
 * as a multi-select filter and a group-by dimension, attribute filters with
 * autocomplete, in-place dashboard edits kept in the URL, and Save as code.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createFixtureApi } from "../__fixtures__/fixtureApi";
import { ENV_PLACE, PROJECT_PLACE, renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

const callsOf = (onCall: ReturnType<typeof vi.fn>, rpc: string) => onCall.mock.calls.filter(([name]) => name === rpc).map(([, req]) => req);

describe("environment is a filter", () => {
  it("reads every environment of the project by default, and the picked ones once chosen", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), {}, PROJECT_PLACE);
    await screen.findByTestId("obs-overview");
    expect(callsOf(onCall, "GetServiceOverview")[0]).toMatchObject({ projectId: "shop", envIds: [], filters: [] });
    expect(screen.getByTestId("obs-env-picker")).toHaveTextContent("All environments");

    await user.click(screen.getByTestId("obs-env-picker"));
    await user.click(screen.getByTestId("obs-env-option-prod"));
    await user.click(screen.getByTestId("obs-env-option-staging"));
    expect(harness.current().envs).toEqual(["prod", "staging"]);
    await waitFor(() => expect(callsOf(onCall, "GetServiceOverview").at(-1)).toMatchObject({ envIds: ["env-prod", "env-staging"] }));
    expect(screen.getByTestId("obs-env-picker")).toHaveTextContent("prod, staging");

    await user.click(screen.getByTestId("obs-env-all"));
    expect(harness.current().envs).toBeUndefined();
  });

  it("pins the env page to its environment, with a way to the same view across all of them", async () => {
    const user = userEvent.setup();
    const onWiden = vi.fn();
    renderPanel(createFixtureApi(), { view: "traces", groupBy: ["service.version"] }, { ...ENV_PLACE, onWiden } as typeof ENV_PLACE);
    expect(await screen.findByTestId("obs-pinned-env")).toHaveTextContent("prod");
    expect(screen.queryByTestId("obs-env-picker")).not.toBeInTheDocument();
    await user.click(screen.getByTestId("obs-widen-envs"));
    expect(onWiden).toHaveBeenCalledWith(expect.objectContaining({ view: "traces", groupBy: ["service.version"], envs: ["prod"] }));
  });
});

describe("group-by", () => {
  it("splits the overview per environment, and compares a service's environments on one chart", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), {}, PROJECT_PLACE);
    await screen.findByTestId("obs-overview");
    await user.click(screen.getByTestId("obs-add-group-by"));
    await user.click(within(screen.getByTestId("obs-add-group-by-popover")).getByTestId("obs-key-deployment.environment.name"));
    expect(harness.current().groupBy).toEqual(["deployment.environment.name"]);
    await screen.findByTestId("obs-service-api-prod");
    expect(screen.getByTestId("obs-service-api-staging")).toBeInTheDocument();
    expect(screen.getByTestId("obs-overview-group-header")).toHaveTextContent("Environment");
    expect(callsOf(onCall, "GetServiceOverview").at(-1)).toMatchObject({ groupBy: ["deployment.environment.name"] });

    await user.click(within(screen.getByTestId("obs-service-api-prod")).getByRole("button", { name: "api" }));
    const detail = await screen.findByTestId("obs-service-detail");
    expect(detail).toHaveAttribute("data-compared", "true");
    expect(detail.querySelector("figcaption")).toHaveTextContent(/dev: latest .*staging: latest .*prod: latest/);
    expect(within(detail).getByTestId("obs-service-latency")).toHaveTextContent("staging");
  });

  it("answers the trace list's RED summary per group, and a group narrows to it", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { view: "traces", groupBy: ["service.version"], f: ["status:error"] }, PROJECT_PLACE);
    const groups = await screen.findByTestId("obs-trace-red-groups");
    expect(callsOf(onCall, "ListTraces")[0]).toMatchObject({ groupBy: ["service.version"], filters: [{ key: "status", op: "eq", value: "error" }] });
    expect(groups).toHaveTextContent("v1.8.2");
    expect(groups).toHaveTextContent("v1.8.3");
    await user.click(within(screen.getByTestId("obs-trace-red-group-v1.8.3")).getByRole("button"));
    expect(harness.current().f).toEqual(["status:error", "service.version:v1.8.3"]);
  });

  it("is not offered on views that list rows", async () => {
    renderPanel(createFixtureApi(), { view: "logs" }, PROJECT_PLACE);
    await screen.findByTestId("obs-scope-bar");
    expect(screen.queryByTestId("obs-group-by")).not.toBeInTheDocument();
  });
});

describe("attribute filters", () => {
  it("adds a filter from autocomplete, sends it as scope data, and removes it", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), {}, PROJECT_PLACE);
    await screen.findByTestId("obs-overview");
    await user.click(screen.getByTestId("obs-add-filter"));
    await user.type(screen.getByTestId("obs-filter-key-input"), "version");
    await user.click(screen.getByTestId("obs-key-service.version"));
    const option = await screen.findByTestId("obs-value-v1.8.3");
    expect(option).toHaveTextContent(/v1\.8\.3\s*2\.1k/);
    expect(callsOf(onCall, "ListAttributeValues")[0]).toMatchObject({ key: "service.version", projectId: "shop" });
    await user.click(option);
    expect(harness.current().f).toEqual(["service.version:v1.8.3"]);
    await waitFor(() =>
      expect(callsOf(onCall, "GetServiceOverview").at(-1)).toMatchObject({ filters: [{ key: "service.version", op: "eq", value: "v1.8.3" }] })
    );
    const chip = screen.getByTestId("obs-filter-chip");
    expect(chip).toHaveTextContent("Version is v1.8.3");
    await user.click(within(chip).getByRole("button", { name: /Remove filter/ }));
    expect(harness.current().f).toBeUndefined();
  });

  it("accepts a typed key and an operator: a declared metric attribute the catalog cannot know", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), {}, PROJECT_PLACE);
    await screen.findByTestId("obs-overview");
    await user.click(screen.getByTestId("obs-add-filter"));
    await user.type(screen.getByTestId("obs-filter-key-input"), "orders.status{Enter}");
    await user.selectOptions(screen.getByTestId("obs-filter-op"), "neq");
    await user.type(screen.getByTestId("obs-filter-value-input"), "failed");
    await user.click(screen.getByTestId("obs-filter-apply"));
    expect(harness.current().f).toEqual(["orders.status!:failed"]);
  });
});

describe("dashboards, semi-dynamic", () => {
  it("re-runs every panel with the group-by and filters, and edits a panel in place into the URL", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(
      createFixtureApi({ onCall }),
      { view: "dashboards", groupBy: ["deployment.environment.name"], f: ["service.version:v1.8.3"] },
      PROJECT_PLACE
    );
    const rate = await screen.findByTestId("obs-panel-request-rate");
    await waitFor(() => expect(rate.querySelector("figcaption")).toHaveTextContent(/dev: latest .*staging: latest .*prod: latest/));
    const req = callsOf(onCall, "QueryPanel").find((r) => r.panel.id === "request-rate");
    expect(req).toMatchObject({ groupBy: ["deployment.environment.name"], filters: [{ key: "service.version", op: "eq", value: "v1.8.3" }] });
    expect(screen.getByTestId("obs-dashboard-changed")).toHaveTextContent("1 filter · grouped by Environment");

    await user.click(screen.getByTestId("obs-panel-edit-request-rate"));
    const editor = await screen.findByTestId("obs-panel-editor");
    await user.clear(within(editor).getByTestId("obs-editor-title"));
    await user.type(within(editor).getByTestId("obs-editor-title"), "Requests by environment");
    await user.selectOptions(within(editor).getByTestId("obs-editor-kind"), "table");
    await user.click(within(editor).getByTestId("obs-editor-apply"));
    expect(harness.current().edit?.["overview/request-rate"]).toMatchObject({ title: "Requests by environment", kind: "table" });
    const edited = await screen.findByTestId("obs-panel-request-rate");
    expect(edited).toHaveAttribute("data-edited", "true");
    expect(edited).toHaveAttribute("data-kind", "table");
    expect(screen.getByTestId("obs-dashboard-changed")).toHaveTextContent("1 panel edited");

    // Reset puts the dashboard's own panel back; hide removes one.
    await user.click(screen.getByTestId("obs-panel-reset-request-rate"));
    expect(harness.current().edit).toBeUndefined();
    await user.click(screen.getByTestId("obs-panel-hide-p99"));
    expect(harness.current().edit).toEqual({ "overview/p99": null });
    await waitFor(() => expect(screen.queryByTestId("obs-panel-p99")).not.toBeInTheDocument());
    await user.click(screen.getByTestId("obs-discard-edits"));
    expect(harness.current().edit).toBeUndefined();
  });

  it("refuses an edit that is not a panel, and says why", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "dashboards" }, PROJECT_PLACE);
    await user.click(await screen.findByTestId("obs-panel-edit-p95"));
    const editor = await screen.findByTestId("obs-panel-editor");
    await user.selectOptions(within(editor).getByTestId("obs-editor-agg"), "sum");
    await user.clear(within(editor).getByTestId("obs-editor-field"));
    await user.click(within(editor).getByTestId("obs-editor-apply"));
    expect(within(editor).getByTestId("obs-editor-problems")).toHaveTextContent("sum needs a field");
    expect(harness.current().edit).toBeUndefined();
  });

  it("adds a panel, and edits one as JSON", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "dashboards" }, PROJECT_PLACE);
    await user.click(await screen.findByTestId("obs-add-panel"));
    let editor = await screen.findByTestId("obs-panel-editor");
    await user.click(within(editor).getByTestId("obs-editor-apply"));
    expect(harness.current().edit?.["overview/custom-1"]).toMatchObject({ kind: "line", series: [{ agg: "count" }] });
    expect(await screen.findByTestId("obs-panel-custom-1")).toHaveTextContent("Added");

    await user.click(screen.getByTestId("obs-panel-edit-requests"));
    editor = await screen.findByTestId("obs-panel-editor");
    await user.click(within(editor).getByTestId("obs-editor-mode-json"));
    const json = within(editor).getByTestId("obs-editor-json") as HTMLTextAreaElement;
    const panel = JSON.parse(json.value);
    await user.clear(json);
    await user.click(json);
    await user.paste(JSON.stringify({ ...panel, title: "All requests", id: "renamed" }));
    await user.click(within(editor).getByTestId("obs-editor-apply"));
    // The id is the panel's own: JSON cannot move an edit to another panel.
    expect(harness.current().edit?.["overview/requests"]).toMatchObject({ title: "All requests", id: "requests" });
  });

  it("saves the dashboard as code: the obsdash file, its path, and what is and is not baked in", async () => {
    const user = userEvent.setup();
    renderPanel(
      createFixtureApi(),
      {
        view: "dashboards",
        envs: ["prod"],
        groupBy: ["service.version"],
        f: ["status:error"],
        edit: { "overview/requests": { id: "requests", title: "All requests", kind: "number", signal: "traces", series: [{ label: "requests", agg: "count" }] } },
      },
      PROJECT_PLACE
    );
    await user.click(await screen.findByTestId("obs-save-as-code-open"));
    const dialog = await screen.findByTestId("obs-save-as-code");
    expect(within(dialog).getByTestId("obs-save-path")).toHaveTextContent("deploy/observability/dashboards/shop-overview.json");
    expect(dialog).toHaveTextContent("replaces the dashboard forge generates");
    expect(dialog).toHaveTextContent("Not saved: the environments, service and time range");
    const doc = JSON.parse(within(dialog).getByTestId("obs-save-json").textContent ?? "");
    // One dashboard file, forge's dashboardFile: the version, then the dashboard's own fields.
    expect(Object.keys(doc)[0]).toBe("version");
    expect(doc).toMatchObject({ version: "forge.dashboards/v1", id: "shop-overview" });
    expect(doc).not.toHaveProperty("dashboards");
    expect(doc).not.toHaveProperty("source");
    const dashboard = doc;
    expect(dashboard.panels.find((p: { id: string }) => p.id === "requests")).toMatchObject({
      title: "All requests",
      where: [{ field: "span.status", op: "eq", value: "error" }],
      groupBy: ["resource.attr.service.version"],
    });
    // The environment selection is a viewing choice, never a predicate.
    expect(JSON.stringify(doc)).not.toContain("env-prod");

    await user.clear(within(dialog).getByTestId("obs-save-name"));
    await user.type(within(dialog).getByTestId("obs-save-name"), "Errors by Version");
    expect(within(dialog).getByTestId("obs-save-path")).toHaveTextContent("deploy/observability/dashboards/errors-by-version.json");
    expect(dialog).not.toHaveTextContent("replaces the dashboard forge generates");
    expect(within(dialog).getByTestId("obs-save-download")).toBeEnabled();
  });
});
