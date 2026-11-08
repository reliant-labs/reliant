// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import {
  NO_MACHINE,
  chatMachineId,
  chatMatchesMachineFilter,
  countByMachine,
  effectiveMachineFilter,
  filterChatsByMachine,
  machineResolver,
  onlyMachine,
  toggleMachine,
} from "../sidebarMachineFilter";

const machine = (daemonId: string, status = DaemonStatus.ACTIVE, isDefault = false) => ({
  daemonId,
  status,
  daemonType: "self_hosted",
  isDefault,
});

// "laptop" is the user's default; "cloud" is a second machine.
const machines = [machine("cloud"), machine("laptop", DaemonStatus.SUSPENDED, true)];
const branchOnCloud = { id: "ws-cloud", is_main: false, daemon_id: "cloud" };
const context = machineResolver(machines, [branchOnCloud, { id: "main", is_main: true }]);

describe("chatMachineId", () => {
  it("is the machine the chat is pinned to", () => {
    expect(chatMachineId({ activeDaemonId: "cloud", worktreeId: "main" }, context)).toBe("cloud");
  });

  it("is the branch workspace's machine for a chat not pinned yet", () => {
    expect(chatMachineId({ worktreeId: "ws-cloud" }, context)).toBe("cloud");
  });

  it("is the default machine for a chat that names none", () => {
    expect(chatMachineId({}, context)).toBe("laptop");
    expect(chatMachineId({ worktreeId: "main" }, context)).toBe("laptop");
    expect(chatMachineId({ worktreeId: "not-cached" }, context)).toBe("laptop");
  });

  it("is No machine for a chat with no machine by design, whatever else it carries", () => {
    expect(chatMachineId({ noMachine: true, activeDaemonId: "cloud" }, context)).toBe(NO_MACHINE);
  });

  it("is nothing when the user has no machine to place it on", () => {
    expect(chatMachineId({}, machineResolver([]))).toBeUndefined();
  });
});

describe("effectiveMachineFilter", () => {
  it("is All machines when nothing is persisted", () => {
    expect(effectiveMachineFilter(null, machines)).toBeNull();
    expect(effectiveMachineFilter(undefined, machines)).toBeNull();
  });

  it("is All machines with one machine, whatever is persisted", () => {
    expect(effectiveMachineFilter(["cloud"], [machine("cloud")])).toBeNull();
  });

  it("drops machines the user no longer has, and keeps No machine", () => {
    expect(effectiveMachineFilter(["gone", "cloud", NO_MACHINE], machines)).toEqual(["cloud", NO_MACHINE]);
  });

  it("is All machines when every persisted machine is gone", () => {
    expect(effectiveMachineFilter(["gone", "also-gone"], machines)).toBeNull();
  });

  it("is All machines for persisted state that is not a list", () => {
    expect(effectiveMachineFilter("cloud" as unknown as string[], machines)).toBeNull();
  });
});

describe("filtering chats", () => {
  const chats = [
    { id: "pinned-cloud", activeDaemonId: "cloud" },
    { id: "branch-cloud", worktreeId: "ws-cloud" },
    { id: "unpinned", worktreeId: "main" },
    { id: "no-machine", noMachine: true },
    { id: "gone-machine", activeDaemonId: "gone" },
  ];
  const ids = (list: { id: string }[]) => list.map((c) => c.id);

  it("lists every chat under All machines, as the same array", () => {
    expect(filterChatsByMachine(chats, null, context)).toBe(chats);
  });

  it("lists a machine's chats, counting unpinned chats as the default machine's", () => {
    expect(ids(filterChatsByMachine(chats, ["cloud"], context))).toEqual(["pinned-cloud", "branch-cloud"]);
    expect(ids(filterChatsByMachine(chats, ["laptop"], context))).toEqual(["unpinned"]);
  });

  it("composes a set of machines, No machine included", () => {
    expect(ids(filterChatsByMachine(chats, ["laptop", NO_MACHINE], context))).toEqual(["unpinned", "no-machine"]);
  });

  it("keeps a chat on a removed machine to All machines alone", () => {
    expect(chatMatchesMachineFilter({ activeDaemonId: "gone" }, ["cloud", "laptop"], context)).toBe(false);
    expect(chatMatchesMachineFilter({ activeDaemonId: "gone" }, null, context)).toBe(true);
  });

  it("counts chats per machine", () => {
    const counts = countByMachine(chats, context, (chat) => chat.id !== "branch-cloud");
    expect(Object.fromEntries(counts)).toEqual({ cloud: 1, laptop: 1, [NO_MACHINE]: 1, gone: 1 });
  });
});

describe("choosing machines", () => {
  const options = ["cloud", "laptop", NO_MACHINE];

  it("turns one machine off from All machines", () => {
    expect(toggleMachine(null, "cloud", options)).toEqual(["laptop", NO_MACHINE]);
  });

  it("turns a machine back on, and every machine on is All machines", () => {
    expect(toggleMachine(["laptop"], "cloud", options)).toEqual(["laptop", "cloud"]);
    expect(toggleMachine(["laptop", NO_MACHINE], "cloud", options)).toBeNull();
  });

  it("refuses to turn off the last machine shown", () => {
    const filter = ["laptop"];
    expect(toggleMachine(filter, "laptop", options)).toBe(filter);
  });

  it("shows one machine alone with Only", () => {
    expect(onlyMachine("cloud", options)).toEqual(["cloud"]);
    expect(onlyMachine("cloud", ["cloud"])).toBeNull();
  });
});
