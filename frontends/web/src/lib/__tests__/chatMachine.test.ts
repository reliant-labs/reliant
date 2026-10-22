// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import {
  DEFAULT_MACHINE,
  NO_MACHINE,
  chatMachineOptions,
  continueBranchPoint,
  defaultChatMachine,
  defaultMachineDaemon,
  hasMachine,
  isUsableMachineForChat,
  startOptionsForMachine,
} from "../chatMachine";

const machine = (status: DaemonStatus, daemonId = "d1", hostname = "laptop", daemonType = "self_hosted") => ({
  daemonId,
  hostname,
  status,
  daemonType,
});

const EVERY_STATUS = [
  DaemonStatus.ACTIVE,
  DaemonStatus.IDLE,
  DaemonStatus.PENDING,
  DaemonStatus.SUSPENDED,
  DaemonStatus.DISCONNECTED,
  DaemonStatus.FAILED,
  DaemonStatus.UNSPECIFIED,
];

describe("hasMachine", () => {
  it("counts every machine the user has, whatever state it is in right now", () => {
    expect(hasMachine([])).toBe(false);
    for (const status of EVERY_STATUS) {
      expect(hasMachine([machine(status)])).toBe(true);
    }
  });
});

describe("isUsableMachineForChat", () => {
  it("counts a machine that serves a chat now, once it is up, or after a send wakes it", () => {
    expect(isUsableMachineForChat(machine(DaemonStatus.ACTIVE))).toBe(true);
    expect(isUsableMachineForChat(machine(DaemonStatus.IDLE))).toBe(true);
    expect(isUsableMachineForChat(machine(DaemonStatus.SUSPENDED))).toBe(true);
    // Starting: provisioning, or restarting during a release. It comes up by
    // itself; nothing has to wake it.
    expect(isUsableMachineForChat(machine(DaemonStatus.PENDING))).toBe(true);
  });

  it("does not count a machine offline, failed or unknown", () => {
    expect(isUsableMachineForChat(machine(DaemonStatus.DISCONNECTED))).toBe(false);
    expect(isUsableMachineForChat(machine(DaemonStatus.FAILED))).toBe(false);
    expect(isUsableMachineForChat(machine(DaemonStatus.UNSPECIFIED))).toBe(false);
  });
});

describe("defaultChatMachine", () => {
  it("is the user's machine whenever they have one, asleep included", () => {
    expect(defaultChatMachine({ daemons: [machine(DaemonStatus.ACTIVE)], loading: false })).toBe(DEFAULT_MACHINE);
    expect(defaultChatMachine({ daemons: [machine(DaemonStatus.SUSPENDED)], loading: false })).toBe(DEFAULT_MACHINE);
  });

  // Prod, 2026-10-09 22:17:30 UTC: the user's only machine was restarting
  // under a release (registry phase provisioning → PENDING). The new chat
  // defaulted to No machine, StartChat persisted chats.no_machine, and the
  // chat said "No machine" for good, while the Files tab and the terminal —
  // which resolve the machine per request — worked again 20 s later when the
  // daemon re-registered. A machine that is not up yet is a wait, not an
  // absence.
  it("is the user's machine while it is starting, reconnecting or failed: never No machine", () => {
    for (const status of [DaemonStatus.PENDING, DaemonStatus.DISCONNECTED, DaemonStatus.FAILED, DaemonStatus.UNSPECIFIED]) {
      expect(defaultChatMachine({ daemons: [machine(status)], loading: false })).toBe(DEFAULT_MACHINE);
    }
  });

  it("is No machine only when the user has no machine at all", () => {
    expect(defaultChatMachine({ daemons: [], loading: false })).toBe(NO_MACHINE);
  });

  it("decides nothing until it can know: loading, or the desktop app's own daemon still registering", () => {
    expect(defaultChatMachine({ daemons: [], loading: true })).toBeUndefined();
    expect(defaultChatMachine({ daemons: [], loading: false, awaitingBundledDaemon: true })).toBeUndefined();
  });

  it("on mobile, preselects no machine when the only machines are asleep or down (§2.5)", () => {
    for (const status of [DaemonStatus.SUSPENDED, DaemonStatus.DISCONNECTED, DaemonStatus.FAILED]) {
      expect(defaultChatMachine({ daemons: [machine(status)], loading: false, surface: "mobile" })).toBe(NO_MACHINE);
    }
    expect(defaultChatMachine({ daemons: [], loading: false, surface: "mobile" })).toBe(NO_MACHINE);
  });

  it("on mobile, is the user's machine when one is awake or starting: a starting machine needs no wake", () => {
    for (const status of [DaemonStatus.ACTIVE, DaemonStatus.IDLE, DaemonStatus.PENDING]) {
      expect(defaultChatMachine({ daemons: [machine(status)], loading: false, surface: "mobile" })).toBe(
        DEFAULT_MACHINE,
      );
    }
  });
});

describe("defaultMachineDaemon", () => {
  // Mirrors the server's default resolution (toolexec resolveDaemonID with no
  // selector), so the picker names the machine an unpinned chat lands on.
  it("prefers a connected machine, and a self-hosted one among connected", () => {
    const cloud = machine(DaemonStatus.ACTIVE, "d-cloud", "cloud", "managed");
    const laptop = machine(DaemonStatus.ACTIVE, "d-laptop", "laptop", "self_hosted");
    const asleep = machine(DaemonStatus.SUSPENDED, "d-sleep", "sleepy", "managed");
    expect(defaultMachineDaemon([asleep, cloud, laptop])?.daemonId).toBe("d-laptop");
    expect(defaultMachineDaemon([asleep, cloud])?.daemonId).toBe("d-cloud");
  });

  it("falls to a starting or asleep machine, then to any machine, and is undefined with none", () => {
    const starting = machine(DaemonStatus.PENDING, "d-start");
    const offline = machine(DaemonStatus.DISCONNECTED, "d-off");
    expect(defaultMachineDaemon([offline, starting])?.daemonId).toBe("d-start");
    expect(defaultMachineDaemon([offline])?.daemonId).toBe("d-off");
    expect(defaultMachineDaemon([])).toBeUndefined();
  });
});

describe("chatMachineOptions", () => {
  it("lists online machines first, then asleep, then the rest", () => {
    const options = chatMachineOptions([
      machine(DaemonStatus.DISCONNECTED, "d-off", "old-box"),
      machine(DaemonStatus.PENDING, "d-start", "new-box"),
      machine(DaemonStatus.SUSPENDED, "d-sleep", "cloud"),
      machine(DaemonStatus.ACTIVE, "d-on", "laptop"),
    ]);
    expect(options.map((o) => o.value)).toEqual(["d-on", "d-sleep", "d-start", "d-off"]);
    expect(options.map((o) => o.usable)).toEqual([true, true, true, false]);
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
