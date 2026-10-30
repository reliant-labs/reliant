import { describe, expect, it } from "vitest";

import { PANEL_CONCURRENCY, PanelScheduler } from "../panelScheduler";

function deferred<T = string>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/** Lets promise callbacks (the scheduler's own `then`) run. */
const settle = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

describe("PanelScheduler", () => {
  it("stays under the server's cap of six", () => {
    expect(PANEL_CONCURRENCY).toBeLessThan(6);
    expect(PANEL_CONCURRENCY).toBeGreaterThanOrEqual(2);
  });

  it("runs at most `max` at once and starts the rest first come, first served", async () => {
    const scheduler = new PanelScheduler(2);
    const started: string[] = [];
    let running = 0;
    let peak = 0;
    const gates = new Map<string, ReturnType<typeof deferred>>();
    const results = ["a", "b", "c", "d", "e"].map((name) => {
      gates.set(name, deferred());
      return scheduler.run(async () => {
        started.push(name);
        peak = Math.max(peak, ++running);
        const value = await gates.get(name)!.promise;
        running--;
        return value;
      });
    });
    expect(started).toEqual(["a", "b"]);
    expect(scheduler.stats).toEqual({ running: 2, queued: 3 });

    gates.get("b")!.resolve("B");
    await settle();
    expect(started).toEqual(["a", "b", "c"]);
    gates.get("a")!.resolve("A");
    gates.get("c")!.resolve("C");
    await settle();
    expect(started).toEqual(["a", "b", "c", "d", "e"]);
    gates.get("d")!.resolve("D");
    gates.get("e")!.resolve("E");
    expect(await Promise.all(results)).toEqual(["A", "B", "C", "D", "E"]);
    expect(peak).toBe(2);
    expect(scheduler.stats).toEqual({ running: 0, queued: 0 });
  });

  it("gives a slot back when a task fails", async () => {
    const scheduler = new PanelScheduler(1);
    const failing = scheduler.run(() => Promise.reject(new Error("boom")));
    const next = scheduler.run(async () => "ok");
    await expect(failing).rejects.toThrow("boom");
    await expect(next).resolves.toBe("ok");
  });

  it("drops a queued task that is cancelled without ever starting it", async () => {
    const scheduler = new PanelScheduler(1);
    const hold = deferred();
    const first = scheduler.run(() => hold.promise);
    let startedSecond = false;
    const cancel = new AbortController();
    const second = scheduler.run(async () => {
      startedSecond = true;
      return "never";
    }, cancel.signal);
    const third = scheduler.run(async () => "third");
    expect(scheduler.stats.queued).toBe(2);

    cancel.abort(new Error("gone"));
    await expect(second).rejects.toThrow("gone");
    expect(scheduler.stats.queued).toBe(1);

    hold.resolve("first");
    await expect(first).resolves.toBe("first");
    await expect(third).resolves.toBe("third");
    expect(startedSecond).toBe(false);
  });

  it("aborts a running task, hands it the signal, and frees its slot at once", async () => {
    const scheduler = new PanelScheduler(1);
    const cancel = new AbortController();
    let seen: AbortSignal | undefined;
    const running = scheduler.run((signal) => {
      seen = signal;
      return new Promise<string>(() => undefined);
    }, cancel.signal);
    const waiting = scheduler.run(async () => "next");
    await settle();
    expect(scheduler.stats).toEqual({ running: 1, queued: 1 });

    cancel.abort(new Error("range changed"));
    await expect(running).rejects.toThrow("range changed");
    expect(seen?.aborted).toBe(true);
    await expect(waiting).resolves.toBe("next");
  });

  it("rejects at once when the signal is already aborted", async () => {
    const scheduler = new PanelScheduler(1);
    const cancel = new AbortController();
    cancel.abort(new Error("already"));
    await expect(scheduler.run(async () => "x", cancel.signal)).rejects.toThrow("already");
    expect(scheduler.stats).toEqual({ running: 0, queued: 0 });
  });

  it("does not release a slot twice when a finished task is aborted later", async () => {
    const scheduler = new PanelScheduler(1);
    const cancel = new AbortController();
    await scheduler.run(async () => "done", cancel.signal);
    cancel.abort();
    const a = deferred();
    const b = deferred();
    const first = scheduler.run(() => a.promise);
    const second = scheduler.run(() => b.promise);
    expect(scheduler.stats).toEqual({ running: 1, queued: 1 });
    a.resolve("a");
    b.resolve("b");
    await Promise.all([first, second]);
  });
});
