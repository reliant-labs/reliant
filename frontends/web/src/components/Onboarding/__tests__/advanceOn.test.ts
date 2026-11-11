/**
 * `watchAdvance`: the semantics of a step's required action, against a fake
 * env. Edges (click, event) fire on the action; levels (route, store, when…)
 * fire on reaching the state while the step is open, and a state that already
 * holds on entry only reports `initiallySatisfied`.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { watchAdvance, whenStore, type AdvanceEnv } from "../advanceOn";
import { tourEvents } from "../tourEvents";
import type { AdvanceOn } from "../types";

function fakeEnv(initial = { pathname: "/", search: {} as Record<string, unknown> }) {
  let location = { ...initial };
  const locationListeners = new Set<() => void>();
  const contextListeners = new Set<() => void>();
  let context: Record<string, unknown> = {};
  const env: AdvanceEnv = {
    getPathname: () => location.pathname,
    getSearch: () => location.search,
    subscribeLocation: (fn) => {
      locationListeners.add(fn);
      return () => locationListeners.delete(fn);
    },
    checklistHas: () => false,
    subscribeChecklist: () => () => undefined,
    getContext: () => context,
    subscribeContext: (fn) => {
      contextListeners.add(fn);
      return () => contextListeners.delete(fn);
    },
    document,
  };
  return {
    env,
    go(pathname: string, search: Record<string, unknown> = {}) {
      location = { pathname, search };
      locationListeners.forEach((fn) => fn());
    },
    setContext(next: Record<string, unknown>) {
      context = next;
      contextListeners.forEach((fn) => fn());
    },
    listenerCount: () => locationListeners.size + contextListeners.size,
  };
}

function watch(conditions: AdvanceOn[], env: AdvanceEnv, target?: string) {
  const onFire = vi.fn();
  const watcher = watchAdvance(conditions, { target }, env, onFire);
  return { onFire, watcher };
}

afterEach(() => {
  document.body.innerHTML = "";
  vi.useRealTimers();
});

describe("watchAdvance", () => {
  it("click: fires (a tick later) when the target or a descendant is clicked", () => {
    vi.useFakeTimers();
    document.body.innerHTML = `<button data-tour="go"><span id="inner">Go</span></button><button id="other"></button>`;
    const { env } = fakeEnv();
    const { onFire, watcher } = watch([{ kind: "click" }], env, "[data-tour='go']");
    document.getElementById("other")!.click();
    vi.runAllTimers();
    expect(onFire).not.toHaveBeenCalled();
    document.getElementById("inner")!.click();
    expect(onFire).not.toHaveBeenCalled(); // deferred: the click's own handler runs first
    vi.runAllTimers();
    expect(onFire).toHaveBeenCalledTimes(1);
    watcher.dispose();
  });

  it("event: fires on a matching tour signal, through the app's event bus", () => {
    const { env } = fakeEnv();
    const { onFire, watcher } = watch(
      [{ kind: "event", event: "workflow:nodeAdded", where: (p) => p.type === "approval" }],
      env,
    );
    tourEvents.emit("workflow:nodeAdded", { nodeId: "n1", type: "agent" });
    expect(onFire).not.toHaveBeenCalled();
    tourEvents.emit("workflow:nodeAdded", { nodeId: "n2", type: "approval", anchor: "splice" });
    expect(onFire).toHaveBeenCalledTimes(1);
    watcher.dispose();
  });

  it("route: reaching it fires; already being there only reports initiallySatisfied", () => {
    const away = fakeEnv({ pathname: "/", search: {} });
    const a = watch([{ kind: "route", path: "/settings/machines" }], away.env);
    expect(a.watcher.initiallySatisfied).toBe(false);
    away.go("/settings/machines/abc");
    expect(a.onFire).toHaveBeenCalledTimes(1);
    a.watcher.dispose();

    const there = fakeEnv({ pathname: "/settings/machines", search: {} });
    const b = watch([{ kind: "route", path: "/settings/machines" }], there.env);
    expect(b.watcher.initiallySatisfied).toBe(true);
    there.go("/settings/machines/x");
    expect(b.onFire).not.toHaveBeenCalled();
    b.watcher.dispose();
  });

  it("when: sees search params, signals since entry, and the journey context", () => {
    const f = fakeEnv();
    const { onFire, watcher } = watch(
      [
        {
          kind: "when",
          test: ({ signals, location, context }) =>
            signals["workflow:triggerDeclared"]?.source === "schedule" &&
            !!signals["workflow:saved"] &&
            location.search.view === "builder" &&
            context.ready === true,
        },
      ],
      f.env,
    );
    tourEvents.emit("workflow:triggerDeclared", { name: "t", source: "schedule" });
    tourEvents.emit("workflow:saved", { slug: "fix-my-pr", triggerSources: ["schedule"] });
    f.go("/workflow/fix-my-pr", { view: "builder" });
    expect(onFire).not.toHaveBeenCalled();
    f.setContext({ ready: true });
    expect(onFire).toHaveBeenCalledTimes(1);
    watcher.dispose();
  });

  it("store: whenStore wires any zustand-shaped store", () => {
    let state = { drafts: 0 };
    const listeners = new Set<(s: typeof state) => void>();
    const store = {
      getState: () => state,
      subscribe: (fn: (s: typeof state) => void) => {
        listeners.add(fn);
        return () => listeners.delete(fn);
      },
    };
    const { env } = fakeEnv();
    const { onFire, watcher } = watch([whenStore(store, (s) => s.drafts > 0)], env);
    state = { drafts: 1 };
    listeners.forEach((fn) => fn(state));
    expect(onFire).toHaveBeenCalledTimes(1);
    watcher.dispose();
    expect(listeners.size).toBe(0);
  });

  it("fires at most once across conditions, and never after dispose", () => {
    const f = fakeEnv();
    const { onFire, watcher } = watch(
      [
        { kind: "event", event: "askAgent:opened" },
        { kind: "route", path: "/forge/demo" },
      ],
      f.env,
    );
    tourEvents.emit("askAgent:opened", { subjectKind: "issue", mode: "handoff" });
    f.go("/forge/demo");
    expect(onFire).toHaveBeenCalledTimes(1);
    watcher.dispose();
    expect(f.listenerCount()).toBe(0);

    const g = fakeEnv();
    const second = watch([{ kind: "event", event: "askAgent:opened" }], g.env);
    second.watcher.dispose();
    tourEvents.emit("askAgent:opened", { subjectKind: "issue", mode: "handoff" });
    expect(second.onFire).not.toHaveBeenCalled();
  });

  it("a throwing predicate counts as not met and doesn't break the emitter", () => {
    const f = fakeEnv();
    const { onFire, watcher } = watch(
      [{ kind: "when", test: () => { throw new Error("boom"); } }],
      f.env,
    );
    expect(() => tourEvents.emit("askAgent:copied", { what: "prompt" })).not.toThrow();
    expect(onFire).not.toHaveBeenCalled();
    watcher.dispose();
  });
});
