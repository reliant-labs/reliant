import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectInstallState, type ProjectDaemonInfo } from "@/api/project-grpc";
import { buildDaemonChoices, daemonLabel, defaultDaemonId, noMachineBlockerFor } from "../daemonChoices";

describe("noMachineBlockerFor", () => {
  const listed = [
    { name: "builtin://agent", needsMachine: ["input `tools` includes shell"] },
    { name: "digest", needsMachine: [] },
    { name: "unanalysed" },
  ];

  it("offers No machine only for a workflow the server said runs without one", () => {
    expect(noMachineBlockerFor(listed, "digest")).toBeUndefined();
    expect(noMachineBlockerFor(listed, "builtin://agent")).toMatch(/needs a machine/);
    // The form stores builtins with or without the prefix.
    expect(noMachineBlockerFor(listed, "agent")).toMatch(/needs a machine/);
  });

  it("treats unknown as needing one: the default workflow, an unlisted one, one with no analysis", () => {
    expect(noMachineBlockerFor(listed, "")).toMatch(/default workflow/);
    expect(noMachineBlockerFor(listed, "not-listed")).toMatch(/needs a machine/);
    expect(noMachineBlockerFor(listed, "unanalysed")).toMatch(/needs a machine/);
  });
});

const daemon = (daemonId: string, hostname: string, status = DaemonStatus.ACTIVE) =>
  create(DaemonInfoSchema, { daemonId, hostname, status });

const install = (
  project_id: string,
  daemon_id: string,
  install_state = ProjectInstallState.INSTALLED,
): ProjectDaemonInfo => ({ project_id, daemon_id, path: "/p", cloned_at: "", install_state });

describe("buildDaemonChoices", () => {
  it("mirrors the server: once a project is installed anywhere, only INSTALLED daemons are eligible", () => {
    const choices = buildDaemonChoices(
      [daemon("a", "alpha"), daemon("b", "beta"), daemon("c", "gamma")],
      [
        install("p", "b"),
        // A failed clone is a row, so the project is tracked, but not installed there.
        install("p", "c", ProjectInstallState.FAILED),
        install("other", "a"),
      ],
      "p",
    );
    expect(choices.map((c) => [c.daemonId, c.eligible, c.installed])).toEqual([
      ["b", true, true],
      ["a", false, false],
      ["c", false, false],
    ]);
    expect(choices[1]!.ineligibleReason).toBe("project not installed");
  });

  it("accepts every daemon for a project with no install rows, online first", () => {
    const choices = buildDaemonChoices(
      [daemon("a", "zeta", DaemonStatus.DISCONNECTED), daemon("b", "beta"), daemon("c", "alpha")],
      [],
      "p",
    );
    expect(choices.every((c) => c.eligible)).toBe(true);
    expect(choices.map((c) => `${c.label}:${c.statusLabel}`)).toEqual([
      "alpha:online",
      "beta:online",
      "zeta:offline",
    ]);
  });
});

describe("defaultDaemonId", () => {
  it("picks the one daemon with the project installed", () => {
    const choices = buildDaemonChoices([daemon("a", "a"), daemon("b", "b")], [install("p", "b")], "p");
    expect(defaultDaemonId(choices)).toBe("b");
  });

  it("picks the only daemon when nothing is tracked", () => {
    expect(defaultDaemonId(buildDaemonChoices([daemon("a", "a")], [], "p"))).toBe("a");
  });

  it("does not guess between several equally valid daemons", () => {
    expect(defaultDaemonId(buildDaemonChoices([daemon("a", "a"), daemon("b", "b")], [], "p"))).toBeUndefined();
    expect(
      defaultDaemonId(
        buildDaemonChoices([daemon("a", "a"), daemon("b", "b")], [install("p", "a"), install("p", "b")], "p"),
      ),
    ).toBeUndefined();
  });

  it("has nothing to pick with no daemons", () => {
    expect(defaultDaemonId([])).toBeUndefined();
  });
});

describe("daemonLabel", () => {
  it("uses the hostname, else a short id", () => {
    expect(daemonLabel(daemon("abc", "laptop"), "abc")).toBe("laptop");
    expect(daemonLabel(undefined, "0123456789abcdef")).toBe("daemon 01234567");
  });
});
