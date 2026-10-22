// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";
import { timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { formatMemoryLimit, heartbeatOomKills, machineCrashSummary, processOomKillNotice } from "../machineMemory";

const GiB = 2 ** 30;

describe("heartbeatOomKills", () => {
  it("reads the kills a heartbeat carries", () => {
    expect(
      heartbeatOomKills({ daemon_id: "d", oom_kills: 2, last_oom_kill_at_ms: 1_792_380_211_000, memory_limit_bytes: 64 * GiB }),
    ).toEqual({ count: 2, lastAtMs: 1_792_380_211_000, limitBytes: 64 * GiB });
  });

  it("is undefined for a heartbeat without kills", () => {
    expect(heartbeatOomKills({ daemon_id: "d", memory_limit_bytes: 64 * GiB })).toBeUndefined();
    expect(heartbeatOomKills({ oom_kills: 1 })).toBeUndefined(); // no time: nothing to date it by
    expect(heartbeatOomKills(undefined)).toBeUndefined();
  });

  it("keeps a kill whose limit is unknown", () => {
    expect(heartbeatOomKills({ oom_kills: 1, last_oom_kill_at_ms: 5 })?.limitBytes).toBe(0);
  });
});

describe("processOomKillNotice", () => {
  it("names the limit and how often", () => {
    expect(processOomKillNotice({ count: 3, lastAtMs: 1, limitBytes: 64 * GiB })).toBe(
      "A process on your machine was killed: the machine hit its 64 GiB memory limit (3 times since the machine started).",
    );
    expect(processOomKillNotice({ count: 1, lastAtMs: 1, limitBytes: 0 })).toBe(
      "A process on your machine was killed: the machine hit its memory limit.",
    );
  });

  it("formats binary sizes", () => {
    expect(formatMemoryLimit(4 * GiB)).toBe("4 GiB");
    expect(formatMemoryLimit(1.5 * GiB)).toBe("1.5 GiB");
  });
});

describe("machineCrashSummary", () => {
  const fmt = (ts?: Timestamp) => (ts ? "Oct 19 03:23" : "—");
  const at = timestampFromDate(new Date("2026-10-19T03:23:47Z"));

  it("says nothing for a machine that never crashed", () => {
    expect(machineCrashSummary({ vmCrashCount: 0, oomKillCount: 0 }, fmt)).toBeUndefined();
    expect(machineCrashSummary(undefined, fmt)).toBeUndefined();
  });

  it("names VM crashes and OOM restarts", () => {
    expect(machineCrashSummary({ lastVmCrashAt: at, vmCrashCount: 5, oomKillCount: 0 }, fmt)).toBe(
      "VM crashed Oct 19 03:23 (5 times)",
    );
    expect(
      machineCrashSummary({ lastVmCrashAt: at, vmCrashCount: 1, lastOomKilledAt: at, oomKillCount: 2 }, fmt),
    ).toBe("VM crashed Oct 19 03:23 (once); Ran out of memory Oct 19 03:23 (2 times)");
  });
});
