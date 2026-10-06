// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import {
  DEFAULT_MACHINE,
  NO_MACHINE,
  chatMachineOptions,
  continueBranchPoint,
  defaultChatMachine,
  hasUsableMachineForChat,
  isUsableMachineForChat,
  startOptionsForMachine,
} from "../chatMachine";

const machine = (status: DaemonStatus, daemonId = "d1", hostname = "laptop") => ({ daemonId, hostname, status });

describe("isUsableMachineForChat (NO_MACHINE_CHATS.md §2.1)", () => {
  it("counts a connected machine and an asleep one, which a send wakes", () => {
    expect(isUsableMachineForChat(machine(DaemonStatus.ACTIVE))).toBe(true);
    expect(isUsableMachineForChat(machine(DaemonStatus.IDLE))).toBe(true);
    expect(isUsableMachineForChat(machine(DaemonStatus.SUSPENDED))).toBe(true);
  });

  it("does not count a machine still provisioning, offline, failed or unknown", () => {
    // Provisioning is the case §2.1 names as falling back to no machine —
    // the one place this differs from onboarding's predicate.
    expect(isUsableMachineForChat(machine(DaemonStatus.PENDING))).toBe(false);
    expect(isUsableMachineForChat(machine(DaemonStatus.DISCONNECTED))).toBe(false);
    expect(isUsableMachineForChat(machine(DaemonStatus.FAILED))).toBe(false);
    expect(isUsableMachineForChat(machine(DaemonStatus.UNSPECIFIED))).toBe(false);
  });

  it("is about any machine the user has", () => {
    expect(hasUsableMachineForChat([])).toBe(false);
    expect(hasUsableMachineForChat([machine(DaemonStatus.PENDING), machine(DaemonStatus.SUSPENDED, "d2")])).toBe(true);
  });
});

describe("defaultChatMachine", () => {
  it("is the user's machine whenever they have a usable one, asleep included", () => {
    expect(defaultChatMachine({ daemons: [machine(DaemonStatus.ACTIVE)], loading: false })).toBe(DEFAULT_MACHINE);
    expect(defaultChatMachine({ daemons: [machine(DaemonStatus.SUSPENDED)], loading: false })).toBe(DEFAULT_MACHINE);
  });

  it("falls back to no machine only when the user has none usable", () => {
    expect(defaultChatMachine({ daemons: [], loading: false })).toBe(NO_MACHINE);
    expect(defaultChatMachine({ daemons: [machine(DaemonStatus.PENDING)], loading: false })).toBe(NO_MACHINE);
  });

  it("decides nothing until it can know: loading, or the desktop app's own daemon still registering", () => {
    expect(defaultChatMachine({ daemons: [], loading: true })).toBeUndefined();
    expect(defaultChatMachine({ daemons: [], loading: false, awaitingBundledDaemon: true })).toBeUndefined();
  });

  it("on mobile, preselects no machine unless one is awake (§2.5)", () => {
    expect(defaultChatMachine({ daemons: [machine(DaemonStatus.SUSPENDED)], loading: false, surface: "mobile" })).toBe(
      NO_MACHINE,
    );
    expect(defaultChatMachine({ daemons: [machine(DaemonStatus.ACTIVE)], loading: false, surface: "mobile" })).toBe(
      DEFAULT_MACHINE,
    );
  });
});

describe("chatMachineOptions", () => {
  it("lists online machines first, then asleep, then the rest", () => {
    const options = chatMachineOptions([
      machine(DaemonStatus.DISCONNECTED, "d-off", "old-box"),
      machine(DaemonStatus.SUSPENDED, "d-sleep", "cloud"),
      machine(DaemonStatus.ACTIVE, "d-on", "laptop"),
    ]);
    expect(options.map((o) => o.value)).toEqual(["d-on", "d-sleep", "d-off"]);
    expect(options.map((o) => o.usable)).toEqual([true, true, false]);
    expect(options[1]).toMatchObject({ label: "cloud", statusLabel: "suspended" });
  });
});

describe("startOptionsForMachine", () => {
  it("sends no_machine and no daemon for No machine, nothing for the default, the daemon otherwise", () => {
    expect(startOptionsForMachine(NO_MACHINE)).toEqual({ no_machine: true });
    expect(startOptionsForMachine(DEFAULT_MACHINE)).toEqual({});
    expect(startOptionsForMachine("d1")).toEqual({ daemon_id: "d1" });
  });
});

describe("continueBranchPoint", () => {
  it("is the latest stored message on the main thread", () => {
    const messages = [
      { id: "m1", thread: "chat-1" },
      { id: "m2", thread: "chat-1" },
      { id: "s1", thread: "spawn-1" },
      { id: "optimistic-user-1", thread: "chat-1" },
    ];
    expect(continueBranchPoint(messages, "chat-1")).toBe("m2");
    expect(continueBranchPoint([], "chat-1")).toBeUndefined();
  });
});
