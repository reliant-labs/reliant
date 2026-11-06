// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's signal tiles: the latency histogram (a bucket opens Traces
 * narrowed to its band) and the recent browser sessions (a row opens its
 * replay). Each tile reads, loads and fails on its own.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import { createFixtureApi } from "../__fixtures__/fixtureApi";
import { latParam, parseLatParam, SessionsTile, TracesTile, TILE_SESSIONS } from "../Home/SignalTiles";
import { renderPanel, renderWithApi } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

const SCOPE = { orgId: "org-1", projectId: "shop", envIds: [], filters: [] };

describe("latency band search key", () => {
  it("round-trips, rounding the band outward so it holds the whole bucket", () => {
    expect(latParam({ loMs: 51.7, hiMs: 80.2 })).toBe("51-81");
    expect(parseLatParam("51-81")).toEqual({ loMs: 51, hiMs: 81 });
    expect(parseLatParam("81-51")).toBeNull();
    expect(parseLatParam("fast")).toBeNull();
    expect(parseLatParam(undefined)).toBeNull();
  });
});

describe("traces tile", () => {
  it("draws the window's traces as a histogram, and a bucket opens its band", async () => {
    const onOpen = vi.fn();
    renderWithApi(createFixtureApi(), <TracesTile scope={SCOPE} range="1h" band={{ onOpen }} all={{ onOpen: vi.fn() }} />);
    await waitFor(() => expect(screen.getByTestId("overview-traces-tile")).toHaveAttribute("data-state", "ready"));
    expect(screen.getByTestId("overview-traces-percentiles")).toHaveTextContent(/p50.*p95.*p99.*failed/);
    const buckets = screen.getAllByTestId("overview-traces-bucket");
    expect(buckets.length).toBeGreaterThan(1);
    await userEvent.click(buckets[0]!);
    const band = onOpen.mock.calls[0]![0] as { loMs: number; hiMs: number };
    expect(band.hiMs).toBeGreaterThan(band.loMs);
    expect(buckets[0]).toHaveAccessibleName(/to .*: \d+ traces?/);
  });

  it("says why it is empty", async () => {
    renderWithApi(createFixtureApi({ empty: true }), <TracesTile scope={SCOPE} range="1h" band={{ onOpen: vi.fn() }} all={{ onOpen: vi.fn() }} />);
    expect(await screen.findByTestId("overview-traces-empty")).toHaveTextContent("No traces in the last hour. Traces appear once a service handles a request");
  });

  it("fails alone, with a retry", async () => {
    renderWithApi(
      createFixtureApi({ failError: new ConnectError("down", Code.Unavailable) }),
      <TracesTile scope={SCOPE} range="1h" band={{ onOpen: vi.fn() }} all={{ onOpen: vi.fn() }} />
    );
    const error = await screen.findByTestId("overview-traces-error", {}, { timeout: 10_000 });
    expect(within(error).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("sessions tile", () => {
  it("lists the newest sessions, each a link to its replay", async () => {
    renderWithApi(
      createFixtureApi(),
      <SessionsTile scope={SCOPE} range="24h" session={{ href: (id) => `/sessions/${id}` }} all={{ href: () => "/sessions" }} />
    );
    const list = await screen.findByRole("list", { name: "Recent browser sessions" });
    const links = within(list).getAllByRole("link");
    expect(links.length).toBeGreaterThan(0);
    expect(links.length).toBeLessThanOrEqual(TILE_SESSIONS);
    expect(links[0]!.getAttribute("href")).toMatch(/^\/sessions\/sess-/);
    expect(links[0]).toHaveAccessibleName(/^Replay .*'s session on \//);
  });

  it("says sessions appear once a frontend is deployed, when there are none", async () => {
    renderWithApi(createFixtureApi({ sessions: "empty" }), <SessionsTile scope={SCOPE} range="1h" session={{ onOpen: vi.fn() }} all={{ onOpen: vi.fn() }} />);
    expect(await screen.findByTestId("overview-sessions-empty")).toHaveTextContent("Sessions appear once your frontend is deployed");
  });
});

describe("the observability overview", () => {
  it("shows both tiles under the services, and a histogram bucket opens Traces narrowed to its band", async () => {
    const harness = renderPanel(createFixtureApi(), {});
    await screen.findByTestId("obs-overview");
    await waitFor(() => expect(screen.getByTestId("overview-traces-tile")).toHaveAttribute("data-state", "ready"));
    expect(screen.getByTestId("overview-sessions-tile")).toBeInTheDocument();
    await userEvent.click(screen.getAllByTestId("overview-traces-bucket")[0]!);
    expect(harness.current().view).toBe("traces");
    expect(parseLatParam(harness.current().lat)).not.toBeNull();
    // Traces opens with the band selected, and clearing it drops the band from the URL.
    await screen.findByTestId("obs-trace-region");
    await userEvent.click(screen.getByTestId("obs-trace-region-clear"));
    await waitFor(() => expect(harness.current().lat).toBeUndefined());
  });
});
