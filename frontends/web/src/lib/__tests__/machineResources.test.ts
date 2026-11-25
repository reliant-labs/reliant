// @vitest-environment node
// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import {
  DaemonProcessStatsSchema,
  DaemonResourceStatsSchema,
  MachineStatsSchema,
} from "@/gen/reliant/v1/tools_daemon_pb";
import { resourceSections } from "../machineResources";

describe("resourceSections", () => {
  it("shows the machine's and the daemon's CPU, memory and open files", () => {
    const sections = resourceSections(
      create(DaemonResourceStatsSchema, {
        machine: create(MachineStatsSchema, {
          load1: 3.96,
          cpus: 8,
          memTotalBytes: 32_000_000_000n,
          memAvailableBytes: 20_000_000_000n,
          cpuBusyRatio: 0.614,
          swapTotalBytes: 4_000_000_000n,
          swapFreeBytes: 3_000_000_000n,
          openFiles: 3000n,
          openFilesLimit: 100_000n,
        }),
        process: create(DaemonProcessStatsSchema, {
          cpuCores: 0.25,
          rssBytes: 300_000_000n,
          openFds: 64n,
          fdLimit: 1024n,
          threads: 30n,
          goroutines: 200n,
        }),
      }),
    );
    expect(sections).toEqual([
      {
        title: "Machine",
        rows: [
          { label: "CPU", value: "61% busy · load 4.0 on 8 CPUs" },
          { label: "Memory", value: "12 GB of 32 GB in use" },
          { label: "Swap", value: "1.0 GB of 4.0 GB in use" },
          { label: "Open files", value: "3,000 of 100,000" },
        ],
      },
      {
        title: "Reliant daemon",
        rows: [
          { label: "CPU", value: "0.25 cores" },
          { label: "Memory", value: "300 MB" },
          { label: "File descriptors", value: "64 of 1,024" },
          { label: "Threads", value: "30" },
        ],
      },
    ]);
  });

  it("leaves out what the daemon did not send, and the section with nothing", () => {
    // A machine whose platform has no readers yet: only goroutines, which
    // the card does not show, and no machine reading at all.
    expect(
      resourceSections(create(DaemonResourceStatsSchema, { process: create(DaemonProcessStatsSchema, { goroutines: 12n }) })),
    ).toEqual([]);
    // No swap configured, and memory not read: only load and open files.
    expect(
      resourceSections(
        create(DaemonResourceStatsSchema, {
          machine: create(MachineStatsSchema, { load1: 1, swapTotalBytes: 0n, swapFreeBytes: 0n, openFiles: 12n }),
        }),
      ),
    ).toEqual([
      {
        title: "Machine",
        rows: [
          { label: "CPU", value: "load 1.0" },
          { label: "Open files", value: "12" },
        ],
      },
    ]);
    expect(resourceSections(undefined)).toEqual([]);
  });
});
