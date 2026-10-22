// Copyright (c) 2025 Reliant Labs

/**
 * The "Coming up" strip (WORKFLOW_UI.md §7.2): one lane per enabled schedule
 * automation, ticks anchored on the server's next fire, collapsed past 8
 * lanes, hidden when nothing is scheduled.
 */

import { describe, expect, it } from "vitest";
import { screen, within } from "@testing-library/react";

import type { Trigger } from "@/api/trigger-grpc";
import { ComingUpTimeline, tickLabel } from "../ComingUpTimeline";
import { laneTicks, timelineModel } from "../comingUp";
import { renderAtRoute } from "./automationTestUtils";

const NOW = Date.parse("2026-10-02T06:30:00Z");
const HOUR = 60 * 60 * 1000;
const iso = (t: number) => new Date(t).toISOString();

function trigger(overrides: Partial<Trigger> & Pick<Trigger, "id">): Trigger {
  return {
    name: overrides.id,
    projectId: "p1",
    enabled: true,
    workflow: "",
    presets: {},
    params: {},
    message: "",
    daemonId: "d1",
    health: { status: "healthy", consecutiveFailures: 0, consecutiveSkips: 0, lastFailureDetail: "" },
    createdAt: "",
    updatedAt: "",
    source: { kind: "schedule", schedule: { cron: ["0 9 * * *"], timezone: "Europe/London", overlap: "skip" } },
    nextFireAt: "2026-10-02T08:00:00Z",
    ...overrides,
  };
}

describe("laneTicks", () => {
  it("enumerates the window's fires from the schedule", () => {
    const ticks = laneTicks(
      trigger({ id: "a", source: { kind: "schedule", schedule: { cron: ["0 */6 * * *"], timezone: "UTC", overlap: "skip" } }, nextFireAt: "2026-10-02T12:00:00Z" }),
      NOW,
      NOW + 24 * HOUR,
    );
    expect(ticks.map(iso)).toEqual([
      "2026-10-02T12:00:00.000Z",
      "2026-10-02T18:00:00.000Z",
      "2026-10-03T00:00:00.000Z",
      "2026-10-03T06:00:00.000Z",
    ]);
  });

  it("anchors on the server's next fire when it disagrees with the arithmetic", () => {
    // The client computes 12:00 next; the server says 14:00 (say, a skip the
    // client cannot know about). The server wins, and nothing before it shows.
    const ticks = laneTicks(
      trigger({ id: "a", source: { kind: "schedule", schedule: { cron: ["0 */6 * * *"], timezone: "UTC", overlap: "skip" } }, nextFireAt: "2026-10-02T14:00:00Z" }),
      NOW,
      NOW + 24 * HOUR,
    );
    expect(ticks.map(iso)).toEqual([
      "2026-10-02T14:00:00.000Z",
      "2026-10-02T18:00:00.000Z",
      "2026-10-03T00:00:00.000Z",
      "2026-10-03T06:00:00.000Z",
    ]);
  });

  it("draws nothing when the server's next fire is past the window", () => {
    expect(laneTicks(trigger({ id: "a", nextFireAt: iso(NOW + 30 * HOUR) }), NOW, NOW + 24 * HOUR)).toEqual([]);
  });
});

describe("timelineModel", () => {
  it("has no lanes with zero enabled schedule automations", () => {
    expect(timelineModel([], NOW).lanes).toEqual([]);
    const model = timelineModel(
      [trigger({ id: "off", enabled: false }), trigger({ id: "no-schedule", source: { kind: "unknown" } })],
      NOW,
    );
    expect(model.lanes).toEqual([]);
    expect(model.hiddenCount).toBe(0);
  });

  it("keeps the 8 soonest lanes past 8 and counts the rest", () => {
    const triggers = Array.from({ length: 11 }, (_, i) =>
      trigger({
        id: `t${i}`,
        // Hour i+1 from now, so t0 is soonest and t10 latest.
        source: { kind: "schedule", schedule: { cron: [`0 ${(7 + i) % 24} * * *`], timezone: "UTC", overlap: "skip" } },
        nextFireAt: iso(Date.parse("2026-10-02T07:00:00Z") + i * HOUR),
      }),
    ).reverse();
    const model = timelineModel(triggers, NOW);
    expect(model.lanes.map((lane) => lane.trigger.id)).toEqual(["t0", "t1", "t2", "t3", "t4", "t5", "t6", "t7"]);
    expect(model.hiddenCount).toBe(3);
  });
});

describe("ComingUpTimeline", () => {
  it("renders nothing with zero enabled automations", async () => {
    renderAtRoute(
      <>
        <span data-testid="mounted" />
        <ComingUpTimeline triggers={[trigger({ id: "off", enabled: false })]} now={NOW} />
      </>,
    );
    // The route has rendered once the sentinel is there.
    await screen.findByTestId("mounted");
    expect(screen.queryByTestId("coming-up-timeline")).toBeNull();
  });

  it("collapses beyond 8 lanes with a +N more line", async () => {
    const triggers = Array.from({ length: 10 }, (_, i) =>
      trigger({ id: `t${i}`, name: `Auto ${i}`, nextFireAt: iso(NOW + (i + 1) * HOUR) }),
    );
    renderAtRoute(<ComingUpTimeline triggers={triggers} now={NOW} />);
    const strip = await screen.findByTestId("coming-up-timeline");
    expect(within(strip).getAllByTestId(/^coming-up-lane-/)).toHaveLength(8);
    expect(within(strip).getByTestId("coming-up-more")).toHaveTextContent("+2 more");
  });

  it("labels a tick with the name, the time and the zone, and lists fires for screen readers", async () => {
    renderAtRoute(
      <ComingUpTimeline triggers={[trigger({ id: "a", name: "Nightly triage" })]} now={NOW} />,
    );
    const strip = await screen.findByTestId("coming-up-timeline");
    // 09:00 Europe/London (BST) is 08:00Z, the server's next fire.
    const tick = within(strip).getAllByTestId("coming-up-tick")[0]!;
    expect(tick).toHaveAttribute("title", "Nightly triage · 09:00 Europe/London");
    expect(within(strip).getByRole("list", { name: "Upcoming runs in the next 24 hours" })).toHaveTextContent(
      "Nightly triage · 09:00 Europe/London",
    );
  });

  it("tickLabel reads in the schedule's zone, not the viewer's", () => {
    expect(tickLabel("Nightly triage", Date.parse("2026-10-02T08:00:00Z"), "Europe/London")).toBe(
      "Nightly triage · 09:00 Europe/London",
    );
    expect(tickLabel("Nightly triage", Date.parse("2026-10-02T08:00:00Z"), "America/New_York")).toBe(
      "Nightly triage · 04:00 America/New_York",
    );
  });
});
