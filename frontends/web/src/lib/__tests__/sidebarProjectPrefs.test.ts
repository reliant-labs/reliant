// @vitest-environment node
import { describe, expect, it } from "vitest";

import {
  parseSidebarProjectSettings,
  sidebarProjectPrefsFor,
  withSidebarProjectChange,
} from "../sidebarProjectPrefs";

describe("sidebarProjectPrefs", () => {
  it("reads one org's entry, and the defaults for any other", () => {
    const settings = parseSidebarProjectSettings(
      JSON.stringify({ o1: { pinned: ["a"], hidden: ["b", "b"], show_all: true } }),
    );
    const o1 = sidebarProjectPrefsFor(settings, "o1");
    expect([...o1.pinned]).toEqual(["a"]);
    expect([...o1.hidden]).toEqual(["b"]);
    expect(o1.showAll).toBe(true);
    const o2 = sidebarProjectPrefsFor(settings, "o2");
    expect(o2.pinned.size + o2.hidden.size).toBe(0);
    expect(o2.showAll).toBe(false);
  });

  it("reads anything unreadable as no entries", () => {
    expect(parseSidebarProjectSettings(undefined)).toEqual({});
    expect(parseSidebarProjectSettings("not json")).toEqual({});
    expect(parseSidebarProjectSettings("[1]")).toEqual({});
    expect(parseSidebarProjectSettings(JSON.stringify({ o1: { pinned: [1, ""], show_all: "yes" } }))).toEqual({ o1: {} });
  });

  it("keeps each org's choices apart, and no org under its own key", () => {
    let settings = withSidebarProjectChange({}, "o1", { kind: "pin", projectId: "a", on: true });
    settings = withSidebarProjectChange(settings, null, { kind: "showAll", on: true });
    expect(settings).toEqual({ o1: { pinned: ["a"] }, "": { show_all: true } });
    expect(sidebarProjectPrefsFor(settings, "o1").showAll).toBe(false);
    expect(sidebarProjectPrefsFor(settings, null).showAll).toBe(true);
  });

  it("makes pin and hide exclusive, and stores nothing for the defaults", () => {
    let settings = withSidebarProjectChange({}, "o1", { kind: "pin", projectId: "a", on: true });
    settings = withSidebarProjectChange(settings, "o1", { kind: "hide", projectId: "a", on: true });
    expect(settings).toEqual({ o1: { hidden: ["a"] } });
    settings = withSidebarProjectChange(settings, "o1", { kind: "pin", projectId: "a", on: true });
    expect(settings).toEqual({ o1: { pinned: ["a"] } });
    settings = withSidebarProjectChange(settings, "o1", { kind: "pin", projectId: "a", on: false });
    expect(settings).toEqual({});
  });
});
