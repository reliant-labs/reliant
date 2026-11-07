// @vitest-environment node
/**
 * The poll registry answers to the ClickHouse quota for telemetry reads only:
 * a key subscribed with `quota: false` (a postgres panel's read) keeps polling
 * at its own rate while the quota is tight or gone, and telemetry polls pause.
 */

import { focusManager, QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ObsPollRegistry } from "../pollRegistry";
import { ObsQuotaStore } from "../quotaStore";

const NOW = Date.UTC(2026, 9, 26, 17, 51, 27);

describe("ObsPollRegistry", () => {
  beforeEach(() => vi.useFakeTimers({ now: NOW }));
  afterEach(() => vi.useRealTimers());

  it("keeps a quota-free key polling while the telemetry quota is exhausted, and pauses the telemetry key", async () => {
    const store = new ObsQuotaStore(() => NOW);
    const registry = new ObsPollRegistry(store);
    const client = new QueryClient();
    const refetch = vi.spyOn(client, "refetchQueries").mockResolvedValue();
    const telemetry = ["observability", "panel", "telemetry"];
    const postgres = ["observability", "panel", "postgres"];
    registry.subscribe(client, telemetry, 1_000);
    registry.subscribe(client, postgres, 1_000, { quota: false });
    const polled = (key: string[]) => refetch.mock.calls.filter(([filters]) => filters?.queryKey === key).length;

    await vi.advanceTimersByTimeAsync(1_000);
    expect(polled(telemetry)).toBe(1);
    expect(polled(postgres)).toBe(1);

    store.report({ limit: "queries", lane: "interactive", resetsAtMs: NOW + 60 * 60_000, soft: false });
    await vi.advanceTimersByTimeAsync(3_000);
    expect(polled(telemetry)).toBe(1);
    expect(polled(postgres)).toBe(4);

    store.clear();
    store.report({ limit: "queries", lane: "interactive", resetsAtMs: NOW + 60 * 60_000, soft: true });
    await vi.advanceTimersByTimeAsync(1_000);
    // Tight: the telemetry poll slows to a quarter, the postgres one does not.
    expect(polled(telemetry)).toBe(1);
    expect(polled(postgres)).toBe(5);
  });

  it("polls nothing while the tab is hidden, and resumes when it is shown", async () => {
    const store = new ObsQuotaStore(() => NOW);
    const registry = new ObsPollRegistry(store);
    const client = new QueryClient();
    const refetch = vi.spyOn(client, "refetchQueries").mockResolvedValue();
    registry.subscribe(client, ["observability", "workloads"], 1_000);
    try {
      focusManager.setFocused(false);
      await vi.advanceTimersByTimeAsync(5_000);
      expect(refetch).not.toHaveBeenCalled();
      focusManager.setFocused(true);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(refetch).toHaveBeenCalledTimes(1);
    } finally {
      focusManager.setFocused(undefined);
    }
  });
});
