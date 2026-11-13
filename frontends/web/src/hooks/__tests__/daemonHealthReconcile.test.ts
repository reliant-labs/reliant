// Copyright (c) 2026 Reliant Labs

/**
 * A heartbeat whose health disagrees with the cached daemon list re-reads the
 * list, so a machine that stops being able to start processes (or recovers)
 * shows it at once rather than a poll later (reliant#1919). A heartbeat with
 * no health — from a gateway that predates it — changes nothing.
 */

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { QueryClient } from "@tanstack/react-query";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { seedDaemonList } from "@/test/daemonRegistry";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { MachineHealthState } from "@/gen/reliant/v1/tools_daemon_pb";
import { DAEMON_LIST_QUERY_KEY } from "../daemon-registry-queries";
import { reconcileDaemonHeartbeat, resetDaemonHeartbeatReconcile } from "../useDaemonStatus";

let client: QueryClient;

beforeEach(() => {
  resetDaemonHeartbeatReconcile();
  client = createTestQueryClient();
  client.setQueryDefaults(DAEMON_LIST_QUERY_KEY, { gcTime: Infinity });
});
afterEach(() => client.clear());

function seed(state: MachineHealthState) {
  seedDaemonList(client, [{ daemonId: "d1", status: DaemonStatus.ACTIVE, health: { state } }]);
}

const invalidated = () => client.getQueryState(DAEMON_LIST_QUERY_KEY)?.isInvalidated ?? false;

describe("a heartbeat's health against the cached list", () => {
  it("re-reads the list when a connected machine became degraded", () => {
    seed(MachineHealthState.OK);
    reconcileDaemonHeartbeat(client, "d1", 1_000, "degraded");
    expect(invalidated()).toBe(true);
  });

  it("re-reads the list when a degraded machine recovered", () => {
    seed(MachineHealthState.DEGRADED);
    reconcileDaemonHeartbeat(client, "d1", 1_000, "ok");
    expect(invalidated()).toBe(true);
  });

  it("leaves the list alone when they agree, or the heartbeat carries no health", () => {
    seed(MachineHealthState.OK);
    reconcileDaemonHeartbeat(client, "d1", 1_000, "ok");
    reconcileDaemonHeartbeat(client, "d1", 1_000);
    expect(invalidated()).toBe(false);
  });
});
