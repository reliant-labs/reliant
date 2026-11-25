// Copyright (c) 2026 Reliant Labs

import { describe, expect, it, vi } from "vitest";
import { defaultParseSearch, defaultStringifySearch } from "@tanstack/react-router";

import { hasParam, idListParam, readParam, retainSearchParams, stringParam, writeParam } from "../searchParams";
import { createTabDefault, stringCodec } from "../tabDefault";
import {
  dropShellScope,
  projectOfHomePath,
  projectsParam,
  retainShellScope,
  sameScope,
  scopeOfIds,
  scopeOfSearch,
  scopeOfUrl,
  withScope,
} from "../shellScope";

/** A Storage over a Map, so a test owns both "tabs" and the shared seed. */
function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    get length() {
      return map.size;
    },
    clear: () => map.clear(),
    getItem: (key) => map.get(key) ?? null,
    key: (index) => [...map.keys()][index] ?? null,
    removeItem: (key) => void map.delete(key),
    setItem: (key, value) => void map.set(key, String(value)),
  };
}

/** The search a URL's query parses to, the router's way. */
const parse = (query: string) => defaultParseSearch(query) as Record<string, unknown>;

describe("searchParams", () => {
  const view = stringParam("view", ["detailed", "simplified"] as const, "simplified");

  it("reads a value, and anything unreadable as the default, never an error", () => {
    expect(readParam({ view: "detailed" }, view)).toBe("detailed");
    expect(readParam({ view: "sideways" }, view)).toBe("simplified");
    expect(readParam({}, view)).toBe("simplified");
    expect(hasParam({ view: "sideways" }, view)).toBe(false);
  });

  it("writes the default as no param at all", () => {
    expect(defaultStringifySearch(writeParam({ a: "x" }, view, "simplified"))).toBe("?a=x");
    expect(defaultStringifySearch(writeParam({}, view, "detailed"))).toBe("?view=detailed");
  });

  it("round-trips an id list through the router's encoding, deduplicated", () => {
    const ids = idListParam("ids");
    const search = writeParam({}, ids, ["a", "b", "a"]);
    expect(readParam(parse(defaultStringifySearch(search)), ids)).toEqual(["a", "b"]);
    // A lone numeric id arrives JSON-parsed as a number.
    expect(readParam(parse("?ids=42"), ids)).toEqual(["42"]);
  });

  it("retains a param across a navigation that does not name it, and lets one that does decide", () => {
    const keep = retainSearchParams([view as never]);
    const final = (dest: Record<string, unknown>) => (search: Record<string, unknown>) => ({ ...dest, _from: search });
    expect(keep({ search: { view: "detailed" }, next: final({ other: 1 }) })).toMatchObject({ view: "detailed", other: 1 });
    expect(keep({ search: { view: "detailed" }, next: final({ view: undefined }) })).toEqual({ view: undefined, _from: { view: "detailed" } });
    expect(keep({ search: {}, next: final({}) })).toEqual({ _from: {} });
  });
});

describe("tabDefault", () => {
  function twoTabs() {
    const shared = memoryStorage();
    const make = (tab: Storage) => createTabDefault({ key: "scope", ...stringCodec, storage: { tab: () => tab, seed: () => shared } });
    return { a: make(memoryStorage()), b: make(memoryStorage()), shared };
  }

  it("reads the tab's own value before the seed, and a new tab starts from the last choice anywhere", () => {
    const { a, b } = twoTabs();
    a.set("one");
    expect(a.get()).toBe("one");
    // b has none of its own: the last choice seeds it, and is then its own.
    expect(b.get()).toBe("one");
    a.set("two");
    expect(a.get()).toBe("two");
    expect(b.get()).toBe("one");
  });

  it("never follows another tab once it has a value of its own", () => {
    const { a, b } = twoTabs();
    b.set("b-choice");
    a.set("a-choice");
    expect(b.get()).toBe("b-choice");
    expect(b.getOwn()).toBe("b-choice");
  });

  it("listens to no storage event", () => {
    const add = vi.spyOn(window, "addEventListener");
    const { a } = twoTabs();
    a.set("x");
    a.get();
    expect(add.mock.calls.some(([type]) => type === "storage")).toBe(false);
    add.mockRestore();
  });

  it("answers undefined while it has no key (signed out), and forgets on undefined", () => {
    let key: string | null = null;
    const tab = memoryStorage();
    const d = createTabDefault({ key: () => key, ...stringCodec, storage: { tab: () => tab, seed: () => memoryStorage() } });
    d.set("x");
    expect(d.get()).toBeUndefined();
    key = "u1";
    d.set("x");
    expect(d.get()).toBe("x");
    d.set(undefined);
    expect(d.getOwn()).toBeUndefined();
  });

  it("survives storage that throws", () => {
    const broken = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("denied"); }, removeItem: () => {} } as unknown as Storage;
    const d = createTabDefault({ key: "k", ...stringCodec, storage: { tab: () => broken, seed: () => broken } });
    expect(() => d.set("x")).not.toThrow();
    expect(d.get()).toBeUndefined();
  });
});

describe("shellScope", () => {
  it("reads all, one and several from ?projects=", () => {
    expect(scopeOfSearch(parse("?projects=all"))).toEqual({ mode: "all" });
    expect(scopeOfSearch(parse("?projects=p1"))).toEqual({ mode: "one", projectId: "p1" });
    expect(scopeOfSearch(parse("?projects=p1,p2,p1"))).toEqual({ mode: "many", projectIds: ["p1", "p2"] });
    expect(scopeOfSearch(parse("?projects=,"))).toBeUndefined();
    expect(scopeOfSearch({})).toBeUndefined();
  });

  it("round-trips every scope through the router's encoding", () => {
    for (const scope of [{ mode: "all" as const }, scopeOfIds(["p1"]), scopeOfIds(["p1", "p2"])]) {
      const query = defaultStringifySearch(withScope({}, scope));
      expect(scopeOfSearch(parse(query))).toEqual(scope);
    }
  });

  it("writes focus only beside several or all", () => {
    expect(withScope({}, { mode: "all" }, "p2")).toMatchObject({ projects: "all", focus: "p2" });
    expect(withScope({ focus: "p2" }, scopeOfIds(["p1"]))).toEqual({ projects: "p1", focus: undefined });
  });

  it("lets the chat home's path name the scope, ahead of a param", () => {
    expect(projectOfHomePath("/project/p1")).toBe("p1");
    expect(projectOfHomePath("/o/acme/project/p%201")).toBe("p 1");
    expect(projectOfHomePath("/projects/p1")).toBeUndefined();
    expect(scopeOfUrl("/project/p1", { projects: "p2,p3" })).toEqual({ scope: { mode: "one", projectId: "p1" }, explicit: true });
    expect(scopeOfUrl("/inbox", { projects: "p2,p3" })).toEqual({ scope: { mode: "many", projectIds: ["p2", "p3"] }, explicit: false });
    expect(scopeOfUrl("/inbox", {})).toBeNull();
  });

  it("compares scopes as sets", () => {
    expect(sameScope(scopeOfIds(["a", "b"]), scopeOfIds(["b", "a"]))).toBe(true);
    expect(sameScope(scopeOfIds(["a"]), { mode: "all" })).toBe(false);
    expect(projectsParam.equals?.({ mode: "all" }, { mode: "all" })).toBe(true);
  });

  it("carries the scope along, and the chat home of one project drops it", () => {
    const final = (dest: Record<string, unknown>) => () => dest;
    const from = { projects: "p1,p2", focus: "p2", q: "x" };
    expect(retainShellScope({ search: from, next: final({}) })).toEqual({ projects: "p1,p2", focus: "p2" });
    // Root middleware wraps the project home's: the home names the params (undefined), so they stay out.
    const home = retainShellScope({ search: from, next: (s) => dropShellScope({ search: s, next: final({}) }) });
    expect(defaultStringifySearch(home)).toBe("");
  });
});
