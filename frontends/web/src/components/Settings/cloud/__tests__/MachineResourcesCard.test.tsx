// Copyright (c) 2026 Reliant Labs

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { DaemonInfoSchema } from "@/gen/reliant/v1/daemon_registry_pb";
import {
  DaemonProcessStatsSchema,
  DaemonResourceStatsSchema,
  MachineStatsSchema,
} from "@/gen/reliant/v1/tools_daemon_pb";
import { MachineResourcesCard } from "../MachineResourcesCard";

describe("MachineResourcesCard", () => {
  it("shows the machine's and the daemon's readings side by side", () => {
    const daemon = create(DaemonInfoSchema, {
      daemonId: "d-1",
      resources: create(DaemonResourceStatsSchema, {
        machine: create(MachineStatsSchema, { cpuBusyRatio: 0.5, memTotalBytes: 8_000_000_000n, memAvailableBytes: 2_000_000_000n }),
        process: create(DaemonProcessStatsSchema, { rssBytes: 250_000_000n, openFds: 40n, fdLimit: 1024n }),
      }),
    });
    render(<MachineResourcesCard daemon={daemon} />);
    const machine = screen.getByRole("region", { name: "Machine" });
    expect(within(machine).getByText("50% busy")).toBeTruthy();
    expect(within(machine).getByText("6.0 GB of 8.0 GB in use")).toBeTruthy();
    const process = screen.getByRole("region", { name: "Reliant daemon" });
    expect(within(process).getByText("250 MB")).toBeTruthy();
    expect(within(process).getByText("40 of 1,024")).toBeTruthy();
  });

  it("renders nothing while the daemon has reported no reading", () => {
    const { container } = render(<MachineResourcesCard daemon={create(DaemonInfoSchema, { daemonId: "d-1" })} />);
    expect(container.firstChild).toBeNull();
  });
});
