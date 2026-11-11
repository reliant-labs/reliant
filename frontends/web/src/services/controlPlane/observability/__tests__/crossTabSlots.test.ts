import { beforeEach, describe, expect, it, vi } from "vitest";

import { browserSlotGate, lockSlotGate, OBS_READ_LOCK_PREFIX, perTabGate, resetPerTabReportForTests, type LockRequester } from "../crossTabSlots";
import { OBS_READ_CONCURRENCY, ReadScheduler, type PageVisibility } from "../readScheduler";

/**
 * navigator.locks as one browser has it: a name is held by one holder at a
 * time across every tab, `ifAvailable` answers null instead of waiting, and
 * waiters are served in order (an aborted waiter leaves the line).
 */
class FakeLocks implements LockRequester {
  private readonly held = new Set<string>();
  private readonly waiters = new Map<string, Array<() => void>>();
  readonly granted: string[] = [];

  async request<T>(name: string, options: { ifAvailable?: boolean; signal?: AbortSignal }, callback: (lock: unknown) => T | Promise<T>): Promise<T> {
    if (this.held.has(name)) {
      if (options.ifAvailable) return callback(null);
      await new Promise<void>((resolve, reject) => {
        const line = this.waiters.get(name) ?? [];
        this.waiters.set(name, line);
        const wake = () => resolve();
        line.push(wake);
        options.signal?.addEventListener(
          "abort",
          () => {
            const at = line.indexOf(wake);
            if (at >= 0) {
              line.splice(at, 1);
              reject(new DOMException("The request was aborted.", "AbortError"));
            }
          },
          { once: true }
        );
      });
    }
    this.held.add(name);
    this.granted.push(name);
    try {
      return await callback({ name });
    } finally {
      const wake = this.waiters.get(name)?.shift();
      if (wake) wake();
      else this.held.delete(name);
    }
  }
}

function deferred<T = string>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise<void>((resolve) => setTimeout(resolve, 0));
};

describe("lockSlotGate", () => {
  it("holds the whole browser to its slots, however many tabs queue reads", async () => {
    // The 2026-11-01 report: many console tabs, each with its own page queue of four.
    const locks = new FakeLocks();
    const tabs = Array.from({ length: 3 }, () => new ReadScheduler(OBS_READ_CONCURRENCY, { gate: lockSlotGate(locks, OBS_READ_CONCURRENCY) }));
    let running = 0;
    let peak = 0;
    const releases: Array<() => void> = [];
    const reads = tabs.flatMap((tab) =>
      Array.from({ length: 5 }, () =>
        tab.run(async () => {
          peak = Math.max(peak, ++running);
          const gate = deferred();
          releases.push(() => gate.resolve("done"));
          await gate.promise;
          running--;
          return "ok";
        })
      )
    );
    await settle();
    expect(running).toBe(OBS_READ_CONCURRENCY);

    while (releases.length > 0) {
      releases.shift()!();
      await settle();
    }
    expect(await Promise.all(reads)).toHaveLength(15);
    expect(peak).toBe(OBS_READ_CONCURRENCY);
    expect(new Set(locks.granted)).toEqual(new Set(Array.from({ length: OBS_READ_CONCURRENCY }, (_, i) => OBS_READ_LOCK_PREFIX + i)));
  });

  it("lets a read that waits for a slot be abandoned: it never runs and rejects with the caller's reason", async () => {
    const locks = new FakeLocks();
    const gate = lockSlotGate(locks, 1);
    const first = deferred();
    const holding = gate.run(() => first.promise);
    await settle();

    const controller = new AbortController();
    let ran = false;
    const waiting = gate.run(async () => {
      ran = true;
      return "late";
    }, controller.signal);
    await settle();
    const reason = new Error("left the page");
    controller.abort(reason);
    await expect(waiting).rejects.toBe(reason);

    first.resolve("first");
    expect(await holding).toBe("first");
    await settle();
    expect(ran).toBe(false);
  });

  it("hands a cancelled read's browser-wide slot to the next tab at once, even when its request never settles", async () => {
    const locks = new FakeLocks();
    const tabA = lockSlotGate(locks, 1);
    const tabB = lockSlotGate(locks, 1);
    const cancel = new AbortController();
    const stuck = tabA.run(() => new Promise<string>(() => {}), cancel.signal);
    await settle();
    let otherRan = false;
    const other = tabB.run(async () => {
      otherRan = true;
      return "b";
    });
    await settle();
    expect(otherRan).toBe(false);

    cancel.abort(new Error("unmounted"));
    await expect(stuck).rejects.toThrow("unmounted");
    await expect(other).resolves.toBe("b");
  });

  it("passes a task's own failure through and gives its slot back", async () => {
    const locks = new FakeLocks();
    const gate = lockSlotGate(locks, 1);
    await expect(gate.run(() => Promise.reject(new Error("boom")))).rejects.toThrow("boom");
    expect(await gate.run(async () => "next")).toBe("next");
  });
});

describe("browserSlotGate without Web Locks: a per-tab limit", () => {
  beforeEach(() => resetPerTabReportForTests());

  it("uses the browser-wide lock slots where the platform has Web Locks, and reports nothing", () => {
    const warn = vi.fn();
    expect(browserSlotGate(OBS_READ_CONCURRENCY, { locks: new FakeLocks(), warn })).not.toBe(perTabGate);
    expect(warn).not.toHaveBeenCalled();
  });

  it("falls back to the per-tab gate and says so once per page, not on every scheduler", () => {
    const warn = vi.fn();
    expect(browserSlotGate(OBS_READ_CONCURRENCY, { locks: undefined, warn })).toBe(perTabGate);
    expect(browserSlotGate(OBS_READ_CONCURRENCY, { locks: undefined, warn })).toBe(perTabGate);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0]![0]).toMatch(new RegExp(`limited per tab \\(${OBS_READ_CONCURRENCY} at a time\\), not across tabs`));
  });

  it("is what jsdom gets: it has no navigator.locks, like an old browser", () => {
    expect(browserSlotGate(OBS_READ_CONCURRENCY)).toBe(perTabGate);
  });

  it("holds each tab to its own cap, and no more: tabs together are not limited", async () => {
    const tabs = Array.from({ length: 2 }, () =>
      new ReadScheduler(OBS_READ_CONCURRENCY, { gate: browserSlotGate(OBS_READ_CONCURRENCY, { locks: undefined, warn: () => {} }) })
    );
    const release = deferred();
    const running = [0, 0];
    const reads = tabs.flatMap((tab, t) =>
      Array.from({ length: OBS_READ_CONCURRENCY + 2 }, () =>
        tab.run(async () => {
          running[t]!++;
          await release.promise;
          return "ok";
        })
      )
    );
    // The per-tab gate starts a read as soon as its tab admits it: no waiting to observe.
    expect(running).toEqual([OBS_READ_CONCURRENCY, OBS_READ_CONCURRENCY]);
    expect(tabs.map((tab) => tab.stats)).toEqual([
      { running: OBS_READ_CONCURRENCY, queued: 2 },
      { running: OBS_READ_CONCURRENCY, queued: 2 },
    ]);

    release.resolve("go");
    expect(await Promise.all(reads)).toHaveLength(2 * (OBS_READ_CONCURRENCY + 2));
  });
});

describe("ReadScheduler in a hidden page", () => {
  function visibility(initiallyHidden: boolean) {
    let hidden = initiallyHidden;
    const listeners: Array<() => void> = [];
    const source: PageVisibility = { hidden: () => hidden, onChange: (listener) => void listeners.push(listener) };
    return {
      source,
      show() {
        hidden = false;
        listeners.forEach((listener) => listener());
      },
      hide() {
        hidden = true;
        listeners.forEach((listener) => listener());
      },
    };
  }

  it("starts nothing until the page is shown, so the tab being read goes first", async () => {
    const page = visibility(true);
    const scheduler = new ReadScheduler(2, { visibility: page.source });
    const started: string[] = [];
    const reads = ["a", "b", "c"].map((name) =>
      scheduler.run(async () => {
        started.push(name);
        return name;
      })
    );
    await settle();
    expect(started).toEqual([]);
    expect(scheduler.stats).toEqual({ running: 0, queued: 3 });

    page.show();
    expect(await Promise.all(reads)).toEqual(["a", "b", "c"]);
  });

  it("lets a read already running finish when the page is hidden, and holds the rest", async () => {
    const page = visibility(false);
    const scheduler = new ReadScheduler(1, { visibility: page.source });
    const gate = deferred();
    const running = scheduler.run(() => gate.promise);
    const started: string[] = [];
    const queued = scheduler.run(async () => {
      started.push("next");
      return "next";
    });
    page.hide();
    gate.resolve("first");
    expect(await running).toBe("first");
    await settle();
    expect(started).toEqual([]);

    page.show();
    expect(await queued).toBe("next");
  });
});
