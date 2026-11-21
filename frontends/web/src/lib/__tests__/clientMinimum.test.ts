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
    const fetchMinimums = vi.fn(async () => minimums("1.9.0", ""));
    stop = watchClientMinimum({
      identity: { kind: "desktop", version: "1.8.3" },
      fetchMinimums,
      onBelowMinimum,
      minGapMs: 0,
    });
    // A second check starts only once the first has finished judging its answer.
    await vi.waitFor(() => {
      window.dispatchEvent(new Event("focus"));
      expect(fetchMinimums).toHaveBeenCalledTimes(2);
    });
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
    // Check i starts only once check i-1 has finished judging its answer
    // (one check at a time), so each answer before the last was no news.
    for (let i = 2; i <= 4; i++) {
      await vi.waitFor(() => {
        window.dispatchEvent(new Event("focus"));
        expect(fetchMinimums).toHaveBeenCalledTimes(i);
      });
      expect(onBelowMinimum).not.toHaveBeenCalled();
    }
    await vi.waitFor(() => expect(onBelowMinimum).toHaveBeenCalledTimes(1));
  });
});
