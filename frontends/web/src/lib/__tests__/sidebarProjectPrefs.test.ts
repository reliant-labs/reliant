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

describe("sidebarProjectPrefs: the remembered chat filter (additive `filter`)", () => {
  it("reads and writes an org's filter, and an empty one stores nothing", () => {
    const read = parseSidebarProjectSettings(JSON.stringify({ o1: { pinned: ["p"], filter: ["a", "b", "a", ""] } }));
    expect(sidebarProjectPrefsFor(read, "o1").filter).toEqual(["a", "b"]);
    expect(sidebarProjectPrefsFor(read, "o2").filter).toEqual([]);

    const written = withSidebarProjectChange(read, "o1", { kind: "filter", projectIds: ["c", "d"] });
    expect(written.o1).toEqual({ pinned: ["p"], filter: ["c", "d"] });
    const cleared = withSidebarProjectChange(written, "o1", { kind: "filter", projectIds: [] });
    expect(cleared.o1).toEqual({ pinned: ["p"] });
  });

  it("an old build's value (no filter) still loads, with no filter", () => {
    const old = parseSidebarProjectSettings(JSON.stringify({ o1: { pinned: ["p"], hidden: ["h"], show_all: true } }));
    expect(sidebarProjectPrefsFor(old, "o1")).toMatchObject({ showAll: true, filter: [] });
  });

  it("keeps keys this build does not know on every change", () => {
    const read = parseSidebarProjectSettings(JSON.stringify({ o1: { pinned: ["p"], future: { x: 1 } } }));
    const written = withSidebarProjectChange(read, "o1", { kind: "pin", projectId: "q", on: true });
    expect(written.o1).toEqual({ pinned: ["p", "q"], future: { x: 1 } });
  });
});
