// Copyright (c) 2026 Reliant Labs

/**
 * A connected machine that cannot start processes reads as degraded, with its
 * daemon's reason; a healthy machine, an old daemon that reports no health,
 * and a machine that is not connected at all do not (reliant#1919).
 */

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { MachineHealthSchema, MachineHealthState } from "@/gen/reliant/v1/tools_daemon_pb";
import { degradedReason, heartbeatHealthDisagrees } from "../machineHealth";

const REASON = "This machine cannot start new processes: its process table is full.";

function machine(status: DaemonStatus, state?: MachineHealthState, reason = "") {
  return create(DaemonInfoSchema, {
    daemonId: "d-1",
    status,
    health: state === undefined ? undefined : create(MachineHealthSchema, { state, reason }),
  });
}

describe("degradedReason", () => {
  it("is the daemon's reason for a connected, degraded machine", () => {
    expect(degradedReason(machine(DaemonStatus.ACTIVE, MachineHealthState.DEGRADED, REASON))).toBe(REASON);
    expect(degradedReason(machine(DaemonStatus.IDLE, MachineHealthState.DEGRADED, REASON))).toBe(REASON);
  });

  it("falls back to a sentence of its own when the daemon gave none", () => {
    expect(degradedReason(machine(DaemonStatus.ACTIVE, MachineHealthState.DEGRADED))).toMatch(/cannot start new processes/);
  });

  it("is null for a healthy machine, an old daemon's machine and an unknown state", () => {
    expect(degradedReason(machine(DaemonStatus.ACTIVE, MachineHealthState.OK))).toBeNull();
    expect(degradedReason(machine(DaemonStatus.ACTIVE))).toBeNull();
    expect(degradedReason(machine(DaemonStatus.ACTIVE, MachineHealthState.UNSPECIFIED))).toBeNull();
    expect(degradedReason(undefined)).toBeNull();
  });

  it("is null for a machine that is not connected: its status already explains it", () => {
    expect(degradedReason(machine(DaemonStatus.DISCONNECTED, MachineHealthState.DEGRADED, REASON))).toBeNull();
    expect(degradedReason(machine(DaemonStatus.SUSPENDED, MachineHealthState.DEGRADED, REASON))).toBeNull();
  });
});

describe("heartbeatHealthDisagrees", () => {
  it("is true when the heartbeat and the cached list disagree", () => {
    expect(heartbeatHealthDisagrees(machine(DaemonStatus.ACTIVE, MachineHealthState.OK), "degraded")).toBe(true);
    expect(heartbeatHealthDisagrees(machine(DaemonStatus.ACTIVE), "degraded")).toBe(true);
    expect(heartbeatHealthDisagrees(machine(DaemonStatus.ACTIVE, MachineHealthState.DEGRADED), "ok")).toBe(true);
  });

  it("is false when they agree, or the heartbeat carries no health (an older gateway)", () => {
    expect(heartbeatHealthDisagrees(machine(DaemonStatus.ACTIVE, MachineHealthState.OK), "ok")).toBe(false);
    expect(heartbeatHealthDisagrees(machine(DaemonStatus.ACTIVE), "ok")).toBe(false);
    expect(heartbeatHealthDisagrees(machine(DaemonStatus.ACTIVE, MachineHealthState.DEGRADED), "degraded")).toBe(false);
    expect(heartbeatHealthDisagrees(machine(DaemonStatus.ACTIVE, MachineHealthState.DEGRADED), undefined)).toBe(false);
    expect(heartbeatHealthDisagrees(machine(DaemonStatus.ACTIVE, MachineHealthState.OK), "rebooting")).toBe(false);
  });
});
