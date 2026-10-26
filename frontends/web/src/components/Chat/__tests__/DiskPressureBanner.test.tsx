// Copyright (c) 2025 Reliant Labs

/**
 * The chat's disk banner says which of the chat machine's volumes is full and
 * what fills it, turns into an error once commands are failing for it, and
 * stays out of the way otherwise. Regression for the 2026-10-11 report: the
 * cache/docker volume was at 100% and every build failed with ENOSPC while the
 * UI said nothing about disk at all.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import { DaemonInfoSchema, DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { DiskGuardState, DiskReportSchema } from "@/gen/reliant/v1/tools_daemon_pb";

const state = vi.hoisted(() => ({ daemons: [] as unknown[], activeId: "" }));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: state.daemons }),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ activeDaemon: state.activeId ? { daemonId: state.activeId } : undefined }),
}));
import { DiskPressureBanner } from "../DiskPressureBanner";

function machine(
  over: { lastNoSpaceAtMs?: bigint; pressure?: boolean; freeBytes?: bigint; guard?: DiskGuardState; reclaimed?: string[] } = {},
): DaemonInfo {
  return create(DaemonInfoSchema, {
    daemonId: "d-1",
    name: "default",
    status: DaemonStatus.ACTIVE,
    disk: create(DiskReportSchema, {
      reportedAtMs: BigInt(Date.now()),
      volumes: [
        { label: "home", paths: ["/home/workspace"], totalBytes: 157_000_000_000n, freeBytes: 124_000_000_000n },
        {
          label: "build caches & docker",
          paths: ["/var/cache/workspace/go-build", "/var/lib/docker"],
          totalBytes: 40_000_000_000n,
          freeBytes: over.freeBytes ?? 2_000_000_000n,
          pressure: over.pressure ?? true,
          lastNoSpaceAtMs: over.lastNoSpaceAtMs ?? 0n,
          guard: over.guard ?? DiskGuardState.UNSPECIFIED,
          reclaimedAtMs: over.reclaimed ? BigInt(Date.now()) : 0n,
          reclaimedBytes: over.reclaimed ? 3_200_000_000n : 0n,
          reclaimed: over.reclaimed ?? [],
          consumers: [
            { kind: "docker_images", label: "Docker images", category: "docker", bytes: 18_000_000_000n },
            { kind: "go_build_cache", label: "Go build cache", category: "caches", bytes: 9_000_000_000n, reclaimableBytes: 4_000_000_000n, clearable: true },
            { kind: "go_mod_cache", label: "Go module cache", category: "caches", bytes: 6_000_000_000n },
          ],
        },
      ],
    }),
  });
}

function mount(daemonId?: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <DiskPressureBanner daemonId={daemonId} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  sessionStorage.clear();
  state.daemons = [];
  state.activeId = "";
});
afterEach(cleanup);

describe("DiskPressureBanner", () => {
  it("says builds are paused, and what the machine already freed, when the disk guard refuses heavy commands", () => {
    state.daemons = [machine({ guard: DiskGuardState.REFUSING_HEAVY, reclaimed: ["Go build cache 3.2 GB"] })];
    mount("d-1");
    const banner = screen.getByTestId("disk-pressure-banner");
    expect(banner).toHaveAttribute("data-tone", "error");
    expect(within(banner).getByText("Builds are paused: your machine's disk is almost full")).toBeInTheDocument();
    const note = within(banner).getByTestId("disk-guard-note");
    expect(note).toHaveTextContent("New builds, test runs and codegen are paused");
    expect(note).toHaveTextContent("Freed 3.2 GB automatically (Go build cache 3.2 GB).");
  });

  it("names the full volume — not the home volume — and what is using it", () => {
    state.daemons = [machine()];
    mount("d-1");
    const banner = screen.getByTestId("disk-pressure-banner");
    expect(banner).toHaveAttribute("data-tone", "warning");
    expect(within(banner).getByText("Your machine's disk is almost full")).toBeInTheDocument();
    expect(within(banner).getByText(/build caches & docker: 38 GB of 40 GB used — Docker 18 GB · Build caches 15 GB/)).toBeInTheDocument();
    expect(within(banner).queryByText(/124 GB/)).not.toBeInTheDocument();
  });

  it("becomes an error once a command failed for want of space", () => {
    state.daemons = [machine({ lastNoSpaceAtMs: BigInt(Date.now() - 30_000), freeBytes: 0n })];
    mount("d-1");
    const banner = screen.getByRole("alert");
    expect(banner).toHaveAttribute("data-tone", "error");
    expect(within(banner).getByText("Commands are failing: your machine's disk is full")).toBeInTheDocument();
  });

  it("says nothing while no volume is under pressure", () => {
    state.daemons = [machine({ pressure: false, freeBytes: 12_000_000_000n })];
    mount("d-1");
    expect(screen.queryByTestId("disk-pressure-banner")).not.toBeInTheDocument();
  });

  it("follows the active machine when the chat names none", () => {
    state.daemons = [machine()];
    state.activeId = "d-1";
    mount();
    expect(screen.getByTestId("disk-pressure-banner")).toBeInTheDocument();
  });

  it("stays dismissed for the same state, and comes back when commands start failing", async () => {
    state.daemons = [machine()];
    const { unmount } = mount("d-1");
    await userEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByTestId("disk-pressure-banner")).not.toBeInTheDocument();
    unmount();

    mount("d-1");
    expect(screen.queryByTestId("disk-pressure-banner")).not.toBeInTheDocument();
    cleanup();

    state.daemons = [machine({ lastNoSpaceAtMs: BigInt(Date.now()) })];
    mount("d-1");
    expect(screen.getByTestId("disk-pressure-banner")).toBeInTheDocument();
  });

  it("Free up space opens the dialog for that volume", async () => {
    state.daemons = [machine()];
    mount("d-1");
    await userEvent.click(screen.getByRole("button", { name: "Free up space" }));
    const dialog = await screen.findByTestId("free-disk-space-dialog");
    expect(within(dialog).getByText("Go build cache")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /Clear 1 cache \(about 4\.0 GB\)/ })).toBeEnabled();
  });
});
