// Copyright (c) 2025 Reliant Labs

/**
 * Profiles embeds speedscope with a text alternative beside it.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";

import { createFixtureApi, FIXTURE_TRACE_ID } from "../__fixtures__/fixtureApi";
import { renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

describe("ProfilesView", () => {
  const created: string[] = [];
  beforeEach(() => {
    // jsdom has no object URLs.
    URL.createObjectURL = vi.fn(() => {
      const url = `blob:http://localhost/${created.length}`;
      created.push(url);
      return url;
    });
    URL.revokeObjectURL = vi.fn();
  });
  afterEach(() => {
    created.length = 0;
  });

  it("asks for a service before drawing anything", async () => {
    renderPanel(createFixtureApi(), { view: "profiles" });
    expect(await screen.findByTestId("obs-profiles-pick")).toHaveTextContent("Choose a service");
  });

  it("embeds speedscope with the profile as a blob URL, and lists the heaviest functions", async () => {
    renderPanel(createFixtureApi(), { view: "profiles", service: "api" });
    const frame = await screen.findByTestId("obs-speedscope");
    expect(frame).toHaveAttribute("title", "Flamegraph: api CPU");
    expect(frame.getAttribute("src")).toMatch(/^\/speedscope\/index\.html#profileURL=blob%3A/);
    const top = screen.getByTestId("obs-top-functions");
    expect(within(top).getAllByRole("row")[1]).toHaveTextContent("syscall.Syscall");
    expect(top).toHaveTextContent("150ms");
  });

  it("offers the profile types the service actually has", async () => {
    renderPanel(createFixtureApi(), { view: "profiles", service: "api" });
    await screen.findByTestId("obs-speedscope");
    const options = within(screen.getByTestId("obs-profile-type")).getAllByRole("option").map((o) => o.getAttribute("value"));
    expect(options).toEqual(["cpu", "alloc_space", "inuse_space", "goroutine"]);
  });

  it("narrows to a trace's samples, and explains an empty answer for short spans", async () => {
    const onCall = vi.fn();
    const api = createFixtureApi({ onCall });
    const real = api.getFlamegraph;
    api.getFlamegraph = async (req) => ({ ...(await real(req)), stackCount: 0, speedscopeJson: "" });
    renderPanel(api, { view: "profiles", service: "api", trace: FIXTURE_TRACE_ID });
    expect(await screen.findByTestId("obs-profiles-empty")).toHaveTextContent("shorter than about 10ms");
    expect(onCall).toHaveBeenCalledWith("GetFlamegraph", expect.objectContaining({ traceId: FIXTURE_TRACE_ID, profileType: "cpu" }));
  });
});
