import { describe, expect, it } from "vitest";

import { OBS_READ_CONCURRENCY, ReadScheduler, SERVER_READ_CAP, VISIBLE_READ_RESERVE } from "../readScheduler";

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

describe("ReadScheduler", () => {
  it("stays under the server's cap, with room for every read the Overview fires at once", () => {
    expect(OBS_READ_CONCURRENCY).toBeLessThan(SERVER_READ_CAP);
    // The Overview: ListWorkloads, GetServiceOverview and three KPI panels.
    expect(OBS_READ_CONCURRENCY - VISIBLE_READ_RESERVE).toBeGreaterThanOrEqual(2);
    expect(OBS_READ_CONCURRENCY).toBeGreaterThanOrEqual(5);
  });

  it("starts every waiting visible read before a background one", async () => {
    const scheduler = new ReadScheduler(2);
    const holds = [deferred(), deferred()];
    void scheduler.run(() => holds[0]!.promise);
    void scheduler.run(() => holds[1]!.promise);
    const started: string[] = [];
    const task = (name: string) => async () => {
      started.push(name);
      return name;
    };
    const queued = [
      scheduler.run(task("poll 1"), undefined, "background"),
      scheduler.run(task("poll 2"), undefined, "background"),
      scheduler.run(task("tile"), undefined, "visible"),
    ];
    holds[0]!.resolve("x");
    await settle();
    expect(started[0]).toBe("tile");
    holds[1]!.resolve("y");
    await Promise.all(queued);
    expect(started).toEqual(["tile", "poll 1", "poll 2"]);
  });

  it("never lets background reads take the slots reserved for visible ones", async () => {
    const scheduler = new ReadScheduler(OBS_READ_CONCURRENCY);
    const hold = deferred();
    let backgroundRunning = 0;
    for (let i = 0; i < OBS_READ_CONCURRENCY; i++) {
      void scheduler.run(async () => {
        backgroundRunning++;
        return hold.promise;
      }, undefined, "background");
    }
    expect(backgroundRunning).toBe(OBS_READ_CONCURRENCY - VISIBLE_READ_RESERVE);
    // A tile that mounts now starts at once, though polls are waiting.
    let tileStarted = false;
    const tile = scheduler.run(async () => {
      tileStarted = true;
      return "tile";
    });
    expect(tileStarted).toBe(true);
    await expect(tile).resolves.toBe("tile");
    hold.resolve("done");
  });

  it("frees the slot of a cancelled read at once, even when its request never settles", async () => {
    const scheduler = new ReadScheduler(1);
    const cancel = new AbortController();
    const stuck = scheduler.run(() => new Promise<string>(() => {}), cancel.signal);
    let nextStarted = false;
    const next = scheduler.run(async () => {
      nextStarted = true;
      return "next";
    });
    expect(nextStarted).toBe(false);
    cancel.abort(new Error("unmounted"));
    await expect(stuck).rejects.toThrow("unmounted");
    await expect(next).resolves.toBe("next");
    expect(nextStarted).toBe(true);
  });

  it("starts nothing while the page is hidden, and visible reads first when it is shown", async () => {
    let hidden = true;
    let onChange = () => {};
    const scheduler = new ReadScheduler(1, { visibility: { hidden: () => hidden, onChange: (listener) => (onChange = listener) } });
    const started: string[] = [];
    const queued = [
      scheduler.run(async () => void started.push("poll"), undefined, "background"),
      scheduler.run(async () => void started.push("tile")),
    ];
    await settle();
    expect(started).toEqual([]);
    hidden = false;
    onChange();
    await Promise.all(queued);
    expect(started).toEqual(["tile", "poll"]);
  });

  it("runs at most `max` at once and starts the rest first come, first served", async () => {
    const scheduler = new ReadScheduler(2);
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
    const scheduler = new ReadScheduler(1);
    const failing = scheduler.run(() => Promise.reject(new Error("boom")));
    const next = scheduler.run(async () => "ok");
    await expect(failing).rejects.toThrow("boom");
    await expect(next).resolves.toBe("ok");
  });

  it("drops a queued task that is cancelled without ever starting it", async () => {
    const scheduler = new ReadScheduler(1);
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
    const scheduler = new ReadScheduler(1);
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
    const scheduler = new ReadScheduler(1);
    const cancel = new AbortController();
    cancel.abort(new Error("already"));
    await expect(scheduler.run(async () => "x", cancel.signal)).rejects.toThrow("already");
    expect(scheduler.stats).toEqual({ running: 0, queued: 0 });
  });

  it("does not release a slot twice when a finished task is aborted later", async () => {
    const scheduler = new ReadScheduler(1);
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
