// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { DiskGuardState, DiskReportSchema, DiskVolumeSchema } from "@/gen/reliant/v1/tools_daemon_pb";
import {
  clearableConsumers,
  consumerSummary,
  diskNotice,
  failingRecently,
  HEAVY_PAUSED,
  pressuredVolume,
  RECENT_NO_SPACE_MS,
  reclaimSummary,
  volumesFullestFirst,
} from "../machineDisk";

const cache = create(DiskVolumeSchema, {
  label: "build caches & docker",
  totalBytes: 40_000_000_000n,
  freeBytes: 2_000_000_000n,
  pressure: true,
  consumers: [
    { kind: "go_build_cache", category: "caches", label: "Go build cache", bytes: 8_000_000_000n, clearable: true, reclaimableBytes: 1n },
    { kind: "docker_images", category: "docker", label: "Docker images", bytes: 13_000_000_000n },
    { kind: "go_mod_cache", category: "caches", label: "Go module cache", bytes: 7_000_000_000n },
    { kind: "docker_build_cache", category: "docker", label: "Docker build cache", bytes: 5_000_000_000n, clearable: true, clearing: true },
  ],
});
const home = create(DiskVolumeSchema, { label: "home", totalBytes: 157_000_000_000n, freeBytes: 124_000_000_000n });

describe("machineDisk", () => {
  it("picks the fullest volume under pressure, never the home volume that has room", () => {
    const report = create(DiskReportSchema, { volumes: [home, cache] });
    expect(volumesFullestFirst(report).map((v) => v.label)).toEqual(["build caches & docker", "home"]);
    expect(pressuredVolume(report)?.label).toBe("build caches & docker");
    expect(pressuredVolume(create(DiskReportSchema, { volumes: [home] }))).toBeUndefined();
    expect(pressuredVolume(undefined)).toBeUndefined();
  });

  it("sums consumers by category, biggest first", () => {
    expect(consumerSummary(cache)).toBe("Docker 18 GB · Build caches 15 GB");
  });

  it("is a warning until a command fails, then an error for a while", () => {
    const now = Date.now();
    expect(diskNotice(cache, now)).toMatchObject({ tone: "warning", title: "Your machine's disk is almost full" });
    const failing = create(DiskVolumeSchema, { ...cache, lastNoSpaceAtMs: BigInt(now - 1000) });
    expect(failingRecently(failing, now)).toBe(true);
    expect(diskNotice(failing, now)).toMatchObject({ tone: "error", title: "Commands are failing: your machine's disk is full" });
    expect(failingRecently(failing, now + RECENT_NO_SPACE_MS)).toBe(false);
  });

  it("says when the machine paused builds and what it freed by itself", () => {
    const now = Date.now();
    // Refusing heavy commands is worth a notice even without the pressure bit.
    const paused = create(DiskVolumeSchema, {
      ...cache,
      pressure: false,
      guard: DiskGuardState.REFUSING_HEAVY,
      reclaimedAtMs: BigInt(now - 60_000),
      reclaimedBytes: 3_200_000_000n,
      reclaimed: ["Go build cache 3.2 GB"],
    });
    expect(pressuredVolume(create(DiskReportSchema, { volumes: [home, paused] }))?.label).toBe("build caches & docker");
    expect(diskNotice(paused, now)).toMatchObject({
      tone: "error",
      title: "Builds are paused: your machine's disk is almost full",
      paused: HEAVY_PAUSED,
      reclaimed: "Freed 3.2 GB automatically (Go build cache 3.2 GB)",
    });
    // Reclaiming alone pauses nothing, and a pass that freed nothing says nothing.
    const reclaiming = create(DiskVolumeSchema, { ...cache, guard: DiskGuardState.RECLAIMING });
    expect(diskNotice(reclaiming, now)).toMatchObject({ tone: "warning", paused: "", reclaimed: "" });
    expect(reclaimSummary(create(DiskVolumeSchema, { ...cache, reclaimedAtMs: 1n, reclaimedBytes: 0n }))).toBe("");
  });

  it("offers only clearable consumers that are not already being cleared", () => {
    expect(clearableConsumers(cache).map((c) => c.kind)).toEqual(["go_build_cache"]);
  });
});
