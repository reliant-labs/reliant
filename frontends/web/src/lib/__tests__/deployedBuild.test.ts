import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { BuildInfo } from "../releaseIdentity";
import {
  buildIdentity,
  fetchDeployedBuild,
  watchForNewBuild,
  POLL_INTERVAL_MS,
  MIN_CHECK_GAP_MS,
} from "../deployedBuild";

const RUNNING: BuildInfo = {
  version: "1.8.2-dev+8bcfc6ee",
  commit: "8bcfc6ee0000000000000000000000000000aaaa",
  ref: "main",
  built: "2026-10-22T10:00:00.000Z",
};
const NEWER: BuildInfo = {
  version: "1.8.2-dev+9c0ffee1",
  commit: "9c0ffee10000000000000000000000000000bbbb",
  ref: "main",
  built: "2026-10-23T10:00:00.000Z",
};

function respond(body: unknown, ok = true) {
  return { ok, json: async () => body } as unknown as Response;
}

function fetchReturning(...answers: Array<unknown | Error>) {
  let i = 0;
  return vi.fn(async () => {
    const a = answers[Math.min(i++, answers.length - 1)];
    if (a instanceof Error) throw a;
    return respond(a);
  });
}

let clock = 0;
const now = () => clock;

/** Lets the watch's fetch and json() promises settle. */
const settle = () => vi.advanceTimersByTimeAsync(0);

function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
}

beforeEach(() => {
  vi.useFakeTimers();
  clock = 1_000_000;
  setVisibility("visible");
});

afterEach(() => {
  vi.useRealTimers();
  setVisibility("visible");
});

describe("buildIdentity", () => {
  it("compares on the commit, so a rebuild of one commit is the same build", () => {
    expect(buildIdentity(RUNNING)).toBe(RUNNING.commit);
    expect(buildIdentity({ ...RUNNING, built: "later" })).toBe(buildIdentity(RUNNING));
  });

  it("falls back to the version, and names nothing for an unstamped build", () => {
    expect(buildIdentity({ ...RUNNING, commit: "unknown" })).toBe(RUNNING.version);
    expect(buildIdentity({ version: "unknown", commit: "unknown" })).toBeNull();
    expect(buildIdentity(null)).toBeNull();
  });
});

describe("fetchDeployedBuild", () => {
  it("bypasses every cache between the tab and the origin", async () => {
    const fetchImpl = fetchReturning(NEWER);
    await expect(fetchDeployedBuild(fetchImpl)).resolves.toEqual(NEWER);
    const [url, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toMatch(/^\/version\.json\?t=\d+$/);
    expect(init.cache).toBe("no-store");
  });

  it("reads every failure as no news", async () => {
    await expect(fetchDeployedBuild(fetchReturning(new TypeError("offline")))).resolves.toBeNull();
    await expect(fetchDeployedBuild(vi.fn(async () => respond(NEWER, false)))).resolves.toBeNull();
    // A dev server or a misrouted path answers with index.html.
    const html = vi.fn(async () => ({ ok: true, json: async () => JSON.parse("<!doctype html>") }) as unknown as Response);
    await expect(fetchDeployedBuild(html)).resolves.toBeNull();
  });
});

describe("watchForNewBuild", () => {
  function start(fetchImpl: ReturnType<typeof fetchReturning>, running: BuildInfo | null = RUNNING) {
    const onNewBuild = vi.fn();
    const stop = watchForNewBuild({ running, onNewBuild, fetchImpl, now });
    return { onNewBuild, stop };
  }

  it("says nothing while the deployed build is the one running", async () => {
    const fetchImpl = fetchReturning(RUNNING);
    const { onNewBuild, stop } = start(fetchImpl);
    await settle();
    clock += POLL_INTERVAL_MS;
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(onNewBuild).not.toHaveBeenCalled();
    stop();
  });

  it("prompts once when a different build is deployed", async () => {
    const fetchImpl = fetchReturning(RUNNING, NEWER);
    const { onNewBuild, stop } = start(fetchImpl);
    await settle();
    expect(onNewBuild).not.toHaveBeenCalled();

    clock += POLL_INTERVAL_MS;
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
    expect(onNewBuild).toHaveBeenCalledTimes(1);
    expect(onNewBuild).toHaveBeenCalledWith(NEWER);

    // Still that build: a dismissed prompt stays dismissed.
    clock += POLL_INTERVAL_MS;
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
    expect(onNewBuild).toHaveBeenCalledTimes(1);
    stop();
  });

  it("says nothing when the deployed build cannot be read", async () => {
    const fetchImpl = fetchReturning(new TypeError("Failed to fetch"));
    const { onNewBuild, stop } = start(fetchImpl);
    await settle();
    clock += POLL_INTERVAL_MS;
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(onNewBuild).not.toHaveBeenCalled();
    stop();
  });

  it("says nothing when the deployed build is unstamped", async () => {
    const { onNewBuild, stop } = start(fetchReturning({ version: "unknown", commit: "unknown" }));
    await settle();
    expect(onNewBuild).not.toHaveBeenCalled();
    stop();
  });

  it("never fetches when this bundle cannot name itself", async () => {
    const fetchImpl = fetchReturning(NEWER);
    const { onNewBuild, stop } = start(fetchImpl, null);
    await settle();
    window.dispatchEvent(new Event("focus"));
    await settle();
    expect(fetchImpl).not.toHaveBeenCalled();
    expect(onNewBuild).not.toHaveBeenCalled();
    stop();
  });

  it("checks when the user returns to the tab, once per burst", async () => {
    const fetchImpl = fetchReturning(RUNNING, NEWER);
    const { onNewBuild, stop } = start(fetchImpl);
    await settle();

    clock += MIN_CHECK_GAP_MS;
    window.dispatchEvent(new Event("focus"));
    document.dispatchEvent(new Event("visibilitychange"));
    await settle();
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(onNewBuild).toHaveBeenCalledWith(NEWER);
    stop();
  });

  it("does not poll a hidden tab", async () => {
    const fetchImpl = fetchReturning(NEWER);
    setVisibility("hidden");
    const { onNewBuild, stop } = start(fetchImpl);
    clock += POLL_INTERVAL_MS;
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
    expect(fetchImpl).not.toHaveBeenCalled();
    expect(onNewBuild).not.toHaveBeenCalled();
    stop();
  });

  it("stops listening when stopped", async () => {
    const fetchImpl = fetchReturning(RUNNING);
    const { stop } = start(fetchImpl);
    await settle();
    stop();
    clock += POLL_INTERVAL_MS;
    window.dispatchEvent(new Event("focus"));
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS);
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });
});
