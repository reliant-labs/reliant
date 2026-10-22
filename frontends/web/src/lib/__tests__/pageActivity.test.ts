import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  CLOCK_JUMP_THRESHOLD_MS,
  PageActivity,
  RESUME_MIN_INACTIVE_MS,
  type ResumeEvent,
} from "../pageActivity";

// A document/window pair the test drives by hand. jsdom's own
// visibilityState is a read-only "visible", which is exactly the state we
// need to leave.
function fakePage() {
  const doc = new EventTarget() as EventTarget & { visibilityState: string };
  doc.visibilityState = "visible";
  const win = new EventTarget();
  return {
    doc,
    win,
    hide() {
      doc.visibilityState = "hidden";
      doc.dispatchEvent(new Event("visibilitychange"));
    },
    show() {
      doc.visibilityState = "visible";
      doc.dispatchEvent(new Event("visibilitychange"));
    },
  };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-08T00:35:00Z"));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("PageActivity clock", () => {
  it("does not count time the page spent hidden", () => {
    const page = fakePage();
    const activity = new PageActivity({ doc: page.doc, win: page.win });
    activity.start();

    vi.advanceTimersByTime(4_000);
    page.hide();
    // ELECTRON-B2: an iOS tab frozen for ten minutes.
    vi.setSystemTime(Date.now() + 619_000);
    page.show();
    vi.advanceTimersByTime(1_000);

    expect(activity.activeNow()).toBe(5_000);
    activity.stop();
  });

  it("drops a wall-clock jump while visible (a laptop that slept with the tab in front)", () => {
    const page = fakePage();
    const activity = new PageActivity({ doc: page.doc, win: page.win });
    const events: ResumeEvent[] = [];
    activity.onResume((e) => events.push(e));
    activity.start();

    vi.advanceTimersByTime(3_000);
    // No visibility event fires: the machine just stopped running.
    vi.setSystemTime(Date.now() + 30 * 60_000);
    vi.advanceTimersByTime(1_000);

    expect(activity.activeNow()).toBeLessThan(6_000);
    expect(events.map((e) => e.reason)).toEqual(["clock-jump"]);
    expect(events[0].inactiveMs).toBeGreaterThan(CLOCK_JUMP_THRESHOLD_MS);
    activity.stop();
  });
});

describe("PageActivity resume events", () => {
  it("reports a return after a real absence, with how long it lasted", () => {
    const page = fakePage();
    const activity = new PageActivity({ doc: page.doc, win: page.win });
    const events: ResumeEvent[] = [];
    activity.onResume((e) => events.push(e));
    activity.start();

    page.hide();
    vi.setSystemTime(Date.now() + 60_000);
    page.show();

    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ reason: "visible", inactiveMs: 60_000 });
    activity.stop();
  });

  it("ignores a tab flick shorter than the resume threshold", () => {
    const page = fakePage();
    const activity = new PageActivity({ doc: page.doc, win: page.win });
    const listener = vi.fn();
    activity.onResume(listener);
    activity.start();

    page.hide();
    vi.setSystemTime(Date.now() + RESUME_MIN_INACTIVE_MS - 1);
    page.show();

    expect(listener).not.toHaveBeenCalled();
    activity.stop();
  });

  it("reports the network coming back, a bfcache restore, and a lost connection", () => {
    const page = fakePage();
    const activity = new PageActivity({ doc: page.doc, win: page.win });
    const reasons: string[] = [];
    activity.onResume((e) => reasons.push(e.reason));
    activity.start();

    page.win.dispatchEvent(new Event("online"));
    const restored = new Event("pageshow") as Event & { persisted: boolean };
    restored.persisted = true;
    page.win.dispatchEvent(restored);
    const firstLoad = new Event("pageshow") as Event & { persisted: boolean };
    firstLoad.persisted = false;
    page.win.dispatchEvent(firstLoad);
    activity.noteConnectionLost();

    expect(reasons).toEqual(["online", "pageshow", "connection-lost"]);
    activity.stop();
  });

  it("keeps notifying the other listeners when one throws", () => {
    const page = fakePage();
    const activity = new PageActivity({ doc: page.doc, win: page.win });
    const second = vi.fn();
    activity.onResume(() => {
      throw new Error("boom");
    });
    activity.onResume(second);
    activity.start();

    activity.noteConnectionLost();
    expect(second).toHaveBeenCalledTimes(1);
    activity.stop();
  });
});
