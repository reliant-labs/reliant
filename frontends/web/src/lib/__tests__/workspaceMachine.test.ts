import { describe, expect, it } from "vitest";
import { DEFAULT_MACHINE, NO_MACHINE } from "../chatMachine";
import {
  choiceLeavesWorkspace,
  resolveChatMachine,
  startChoice,
  workspaceFilterMachine,
  workspaceMachine,
  workspacesOnMachine,
} from "../workspaceMachine";

const machines = new Set(["daemon-a", "daemon-b"]);
const main = { id: "main", is_main: true };
const onA = { id: "wt-a", is_main: false, daemon_id: "daemon-a" };
const onB = { id: "wt-b", is_main: false, daemon_id: "daemon-b" };
const unknown = { id: "wt-old", is_main: false };

describe("workspaceMachine", () => {
  it("is the machine holding a branch workspace", () => {
    expect(workspaceMachine(onB, machines)).toBe("daemon-b");
  });

  it("pins nothing for the main checkout, an old row, or a machine the user no longer has", () => {
    expect(workspaceMachine(main, machines)).toBeUndefined();
    expect(workspaceMachine({ ...main, daemon_id: "daemon-a" }, machines)).toBeUndefined();
    expect(workspaceMachine(unknown, machines)).toBeUndefined();
    expect(workspaceMachine({ id: "x", is_main: false, daemon_id: "removed" }, machines)).toBeUndefined();
    expect(workspaceMachine(undefined, machines)).toBeUndefined();
  });
});

describe("resolveChatMachine", () => {
  it("runs a chat in a branch workspace on that workspace's machine, whatever was picked", () => {
    // The reported bug: workspace on B, machine A picked.
    expect(resolveChatMachine({ choice: "daemon-a", fallback: DEFAULT_MACHINE, workspaceDaemonId: "daemon-b" })).toEqual({
      value: "daemon-b",
      lockedBy: "daemon-b",
    });
    expect(resolveChatMachine({ choice: null, fallback: DEFAULT_MACHINE, workspaceDaemonId: "daemon-b" }).value).toBe("daemon-b");
  });

  it("keeps No machine: the chat then runs in the main checkout", () => {
    expect(resolveChatMachine({ choice: NO_MACHINE, fallback: DEFAULT_MACHINE, workspaceDaemonId: "daemon-b" })).toEqual({
      value: NO_MACHINE,
    });
  });

  it("uses the choice, then the default, when the workspace pins nothing", () => {
    expect(resolveChatMachine({ choice: "daemon-a", fallback: DEFAULT_MACHINE, workspaceDaemonId: undefined }).value).toBe("daemon-a");
    expect(resolveChatMachine({ choice: null, fallback: DEFAULT_MACHINE, workspaceDaemonId: undefined }).value).toBe(DEFAULT_MACHINE);
  });
});

describe("startChoice", () => {
  it("leaves a workspace-decided machine to the server, which pins the chat there", () => {
    expect(startChoice({ value: "daemon-b", lockedBy: "daemon-b" })).toBe(DEFAULT_MACHINE);
  });

  it("sends a machine the user chose", () => {
    expect(startChoice({ value: "daemon-a" })).toBe("daemon-a");
    expect(startChoice({ value: NO_MACHINE })).toBe(NO_MACHINE);
  });
});

describe("workspacesOnMachine", () => {
  const all = [onA, onB, unknown];

  it("offers only the chosen machine's workspaces, and old rows whose machine is unknown", () => {
    expect(workspacesOnMachine(all, "daemon-a").map((w) => w.id)).toEqual(["wt-a", "wt-old"]);
  });

  it("offers every workspace when no machine was chosen", () => {
    expect(workspacesOnMachine(all, undefined).map((w) => w.id)).toEqual(["wt-a", "wt-b", "wt-old"]);
  });
});

describe("workspaceFilterMachine", () => {
  it("narrows to the workspace's machine, else an explicit choice, else nothing", () => {
    expect(workspaceFilterMachine("daemon-a", { value: "daemon-b", lockedBy: "daemon-b" })).toBe("daemon-b");
    expect(workspaceFilterMachine("daemon-a", { value: "daemon-a" })).toBe("daemon-a");
    expect(workspaceFilterMachine(null, { value: DEFAULT_MACHINE })).toBeUndefined();
    expect(workspaceFilterMachine(DEFAULT_MACHINE, { value: DEFAULT_MACHINE })).toBeUndefined();
    expect(workspaceFilterMachine(NO_MACHINE, { value: NO_MACHINE })).toBeUndefined();
  });
});

describe("choiceLeavesWorkspace", () => {
  it("is true for another machine, so the composer falls back to the main checkout", () => {
    expect(choiceLeavesWorkspace("daemon-a", "daemon-b")).toBe(true);
    expect(choiceLeavesWorkspace(DEFAULT_MACHINE, "daemon-b")).toBe(true);
  });

  it("is false for the workspace's own machine, No machine, or a workspace that pins nothing", () => {
    expect(choiceLeavesWorkspace("daemon-b", "daemon-b")).toBe(false);
    expect(choiceLeavesWorkspace(NO_MACHINE, "daemon-b")).toBe(false);
    expect(choiceLeavesWorkspace("daemon-a", undefined)).toBe(false);
  });
});
