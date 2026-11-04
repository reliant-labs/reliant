// Copyright (c) 2025 Reliant Labs

/**
 * The project-level Observability area (owner decisions 1 and 2): environment
 * as a multi-select filter and a group-by dimension, and attribute filters
 * with autocomplete.
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
  it("reads every environment of the project by default, with no picker of its own (the top bar scopes it)", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), {}, PROJECT_PLACE);
    await screen.findByTestId("obs-overview");
    expect(callsOf(onCall, "GetServiceOverview")[0]).toMatchObject({ projectId: "shop", envIds: [], filters: [] });
    expect(screen.queryByTestId("obs-env-picker")).not.toBeInTheDocument();
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
    expect(screen.getByRole("columnheader", { name: "Environment" })).toBeInTheDocument();
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
