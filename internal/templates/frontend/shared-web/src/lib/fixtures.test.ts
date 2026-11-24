import { afterEach, describe, expect, it, vi } from "vitest";

import { lazyTransport, runtimeFixtures } from "./fixtures";

import type { Transport } from "@connectrpc/connect";

type ConfigWindow = { __FORGE_CONFIG__?: Record<string, unknown> };

function setConfig(cfg: Record<string, unknown> | undefined) {
  (window as unknown as ConfigWindow).__FORGE_CONFIG__ = cfg;
}

describe("runtimeFixtures", () => {
  afterEach(() => setConfig(undefined));

  it("is null when the runtime config names no scenario", () => {
    setConfig(undefined);
    expect(runtimeFixtures()).toBeNull();
    setConfig({ API_URL: "https://api.example" });
    expect(runtimeFixtures()).toBeNull();
    setConfig({ FIXTURES_SCENARIO: "" });
    expect(runtimeFixtures()).toBeNull();
  });

  it("reads the scenario and keeps writes refused unless told exactly false", () => {
    setConfig({ FIXTURES_SCENARIO: "shop", FIXTURES_READ_ONLY: "true" });
    expect(runtimeFixtures()).toEqual({ scenario: "shop", readOnly: true });
    setConfig({ FIXTURES_SCENARIO: "shop" });
    expect(runtimeFixtures()).toEqual({ scenario: "shop", readOnly: true });
    setConfig({ FIXTURES_SCENARIO: "shop", FIXTURES_READ_ONLY: "no" });
    expect(runtimeFixtures()?.readOnly).toBe(true);
    setConfig({ FIXTURES_SCENARIO: "shop", FIXTURES_READ_ONLY: "false" });
    expect(runtimeFixtures()).toEqual({ scenario: "shop", readOnly: false });
  });
});

describe("lazyTransport", () => {
  it("loads the transport once, on the first call, and delegates to it", async () => {
    const unary = vi.fn(async () => ({}) as Awaited<ReturnType<Transport["unary"]>>);
    const inner = { unary, stream: vi.fn() } as unknown as Transport;
    const load = vi.fn(async () => inner);
    const t = lazyTransport(load);
    expect(load).not.toHaveBeenCalled();

    const call = (t.unary as (...args: unknown[]) => Promise<unknown>).bind(t);
    await call("m", undefined, undefined, undefined, {});
    await call("m", undefined, undefined, undefined, {});
    expect(load).toHaveBeenCalledTimes(1);
    expect(unary).toHaveBeenCalledTimes(2);
  });
});
