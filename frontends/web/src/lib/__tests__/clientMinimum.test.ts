import { afterEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ClientMinimumSchema, ClientMinimumsSchema } from "@/gen/reliant/v1/system_pb";
import { watchClientMinimum } from "../clientMinimum";

const minimums = (web: string, desktop = "") =>
  create(ClientMinimumsSchema, {
    web: create(ClientMinimumSchema, { minVersion: web, upgradeUrl: "https://reliantlabs.io" }),
    desktop: create(ClientMinimumSchema, { minVersion: desktop }),
  });

let stop: (() => void) | undefined;
afterEach(() => {
  stop?.();
  stop = undefined;
});

describe("watchClientMinimum", () => {
  it("reports a build below its kind's served minimum, once", async () => {
    const onBelowMinimum = vi.fn();
    const fetchMinimums = vi.fn(async () => minimums("1.9.0"));
    stop = watchClientMinimum({
      identity: { kind: "web", version: "1.8.3" },
      fetchMinimums,
      onBelowMinimum,
      minGapMs: 0,
    });
    await vi.waitFor(() => expect(onBelowMinimum).toHaveBeenCalledTimes(1));
    expect(onBelowMinimum.mock.calls[0][0]).toMatchObject({ minVersion: "1.9.0" });

    // It stopped watching: a focus does not ask again.
    window.dispatchEvent(new Event("focus"));
    await Promise.resolve();
    expect(fetchMinimums).toHaveBeenCalledTimes(1);
  });

  it("compares the desktop app with the desktop minimum, not the web one", async () => {
    const onBelowMinimum = vi.fn();
    stop = watchClientMinimum({
      identity: { kind: "desktop", version: "1.8.3" },
      fetchMinimums: async () => minimums("1.9.0", ""),
      onBelowMinimum,
      minGapMs: 0,
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(onBelowMinimum).not.toHaveBeenCalled();
  });

  it("treats an older server, a failed read, and a build at the floor as no news, and asks again on focus", async () => {
    const onBelowMinimum = vi.fn();
    const answers = [
      () => Promise.resolve(undefined),
      () => Promise.reject(new Error("offline")),
      () => Promise.resolve(minimums("1.8.3")),
      () => Promise.resolve(minimums("1.9.0")),
    ];
    const fetchMinimums = vi.fn(() => answers.shift()!());
    stop = watchClientMinimum({
      identity: { kind: "web", version: "1.8.3" },
      fetchMinimums,
      onBelowMinimum,
      minGapMs: 0,
    });
    for (let i = 1; i <= 3; i++) {
      await vi.waitFor(() => expect(fetchMinimums).toHaveBeenCalledTimes(i));
      await new Promise((r) => setTimeout(r, 0));
      expect(onBelowMinimum).not.toHaveBeenCalled();
      window.dispatchEvent(new Event("focus"));
    }
    await vi.waitFor(() => expect(onBelowMinimum).toHaveBeenCalledTimes(1));
  });
});
