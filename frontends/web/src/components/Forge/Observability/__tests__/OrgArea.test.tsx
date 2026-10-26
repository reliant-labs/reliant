// Copyright (c) 2025 Reliant Labs

/**
 * The org place (/forge/observability): app and environment are filters over
 * the whole org, and a selection that owns no environment reads nothing
 * rather than falling through to "everything".
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { createFixtureApi } from "../__fixtures__/fixtureApi";
import { ORG_PLACE, renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

const callsOf = (onCall: ReturnType<typeof vi.fn>, rpc: string) => onCall.mock.calls.filter(([name]) => name === rpc).map(([, req]) => req);

describe("org place", () => {
  it("reads the whole org by default, with no project", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), {}, ORG_PLACE);
    await screen.findByTestId("obs-overview");
    const [req] = callsOf(onCall, "GetServiceOverview");
    expect(req.envIds).toEqual([]);
    expect(req.projectId).toBeUndefined();
    expect(screen.getByTestId("obs-app-picker")).toHaveTextContent("All apps");
    expect(screen.getByTestId("obs-env-picker")).toHaveTextContent("All environments");
  });

  it("scopes to one app: its project id and its environments", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { apps: ["shop"] }, ORG_PLACE);
    await screen.findByTestId("obs-overview");
    expect(callsOf(onCall, "GetServiceOverview")[0]).toMatchObject({ projectId: "shop", envIds: ["env-shop-prod", "env-shop-dev"] });
  });

  it("sends no project for several apps, and every environment of them", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { apps: ["shop", "api"] }, ORG_PLACE);
    await screen.findByTestId("obs-overview");
    const req = callsOf(onCall, "GetServiceOverview")[0];
    expect(req.projectId).toBeUndefined();
    expect(req.envIds).toEqual(["env-shop-prod", "env-shop-dev", "env-api-prod"]);
  });

  it("picks apps in the bar and drops picked envs that left the selection", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    const harness = renderPanel(createFixtureApi({ onCall }), { envs: ["shop/prod", "api/prod"] }, ORG_PLACE);
    await screen.findByTestId("obs-overview");
    await user.click(screen.getByTestId("obs-app-picker"));
    await user.click(screen.getByTestId("obs-app-option-shop"));
    expect(harness.current().apps).toEqual(["shop"]);
    expect(harness.current().envs).toEqual(["shop/prod"]);
    await waitFor(() => expect(callsOf(onCall, "GetServiceOverview").at(-1)).toMatchObject({ projectId: "shop", envIds: ["env-shop-prod"] }));
    // the env picker offers only the selected app's environments, by app/env name
    await user.click(screen.getByTestId("obs-env-picker"));
    expect(screen.getByTestId("obs-env-option-shop/dev")).toBeInTheDocument();
    expect(screen.queryByTestId("obs-env-option-api/prod")).not.toBeInTheDocument();
  });

  it("queries nothing for an app with no environments, and says so", async () => {
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { apps: ["web"] }, ORG_PLACE);
    expect(await screen.findByTestId("obs-no-app-environments")).toHaveTextContent("No environments recorded for web");
    expect(callsOf(onCall, "GetServiceOverview")).toHaveLength(0);
    expect(callsOf(onCall, "ListReleases")).toHaveLength(0);
  });
});
