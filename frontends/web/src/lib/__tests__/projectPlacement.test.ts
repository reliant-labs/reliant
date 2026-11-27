// @vitest-environment node
import { describe, expect, it } from "vitest";

import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import {
  canChoose,
  canSetUpElsewhere,
  canStartOn,
  isSettingUpAnywhere,
  moveSourceDaemonIds,
  nestedRepoInstalls,
  nestedRepoLabel,
  preferredDaemonIds,
  projectPlacement,
  sectionMachines,
  setUpPathOn,
  startBlockedReason,
} from "../projectPlacement";

const row = (project_id: string, daemon_id: string, install_state: ProjectInstallState) => ({
  project_id,
  daemon_id,
  install_state,
});

// The owner's report: "temp" is on the laptop, the cicd machine has never
// seen it, and a clone onto the gpu box is in flight.
const installs = [
  row("temp", "laptop", ProjectInstallState.INSTALLED),
  row("temp", "gpu", ProjectInstallState.INSTALLING),
  row("temp", "old", ProjectInstallState.FAILED),
  row("other", "cicd", ProjectInstallState.INSTALLED),
];

describe("projectPlacement", () => {
  it("reads each machine's state for one project only", () => {
    const temp = projectPlacement(installs, "temp");
    expect(temp.known).toBe(true);
    expect(temp.stateOn("laptop")).toBe("ready");
    expect(temp.stateOn("gpu")).toBe("setting_up");
    expect(temp.stateOn("old")).toBe("failed");
    // Another project's install on cicd says nothing about temp.
    expect(temp.stateOn("cicd")).toBe("absent");
  });

  it("is not known for a project with no install record anywhere, and then constrains nothing", () => {
    const fresh = projectPlacement(installs, "fresh");
    expect(fresh.known).toBe(false);
    expect(canStartOn(fresh, "cicd")).toBe(true);
    expect(canChoose(fresh, "cicd")).toBe(true);
    expect(startBlockedReason({ placement: fresh, daemonId: "cicd", projectName: "fresh", machineName: "cicd" })).toBeNull();
  });

  it("lets a chat start only where the checkout is, and be chosen where it is landing", () => {
    const temp = projectPlacement(installs, "temp");
    expect(canStartOn(temp, "laptop")).toBe(true);
    expect(canStartOn(temp, "gpu")).toBe(false);
    expect(canChoose(temp, "gpu")).toBe(true);
    expect(canChoose(temp, "old")).toBe(false);
    expect(canChoose(temp, "cicd")).toBe(false);
  });
});

describe("startBlockedReason", () => {
  const temp = projectPlacement(installs, "temp");
  const reason = (daemonId: string | undefined) =>
    startBlockedReason({ placement: temp, daemonId, projectName: "temp", machineName: daemonId ?? "" });

  it("never blocks a ready machine, or one the server resolves itself", () => {
    expect(reason("laptop")).toBeNull();
    expect(reason(undefined)).toBeNull();
  });

  it("says what to do instead of a bare \"isn't on\"", () => {
    expect(reason("cicd")).toBe("temp isn't on cicd yet. Add it there, or pick a machine it's on.");
    expect(reason("gpu")).toBe("Setting up temp on gpu. You can send once it's there.");
    expect(reason("old")).toBe("Setting up temp on old failed. Try again, or pick a machine it's on.");
  });

  it("offers no add for a plain folder, which has nothing to clone from", () => {
    expect(
      startBlockedReason({ placement: temp, daemonId: "cicd", projectName: "temp", machineName: "cicd", canClone: false }),
    ).toBe("temp is a folder on another machine, so it can't be copied to cicd. Pick a machine it's on, or choose a folder already on cicd.");
  });

  it("names the machine's reason for a failed clone", () => {
    const failed = projectPlacement(
      [{ ...row("cp", "ovh", ProjectInstallState.FAILED), install_error: "failed to create directory: mkdir /Users: permission denied" }],
      "cp",
    );
    expect(failed.failureOn("ovh")).toBe("failed to create directory: mkdir /Users: permission denied");
    expect(
      startBlockedReason({ placement: failed, daemonId: "ovh", projectName: "control-plane", machineName: "ovh-build-1" }),
    ).toBe(
      "Setting up control-plane on ovh-build-1 failed: failed to create directory: mkdir /Users: permission denied. Try again, or pick a machine it's on.",
    );
  });
});

describe("setUpPathOn", () => {
  const macPath = "/Users/owner/src/reliant-labs/control-plane";
  const platforms: Record<string, string> = { mac1: "darwin", mac2: "darwin", ovh: "linux", box: "linux", odd: "" };
  const platformOf = (id: string) => platforms[id];
  const installed = (daemon: string, path: string) => ({ ...row("cp", daemon, ProjectInstallState.INSTALLED), path });
  const pathOn = (daemonId: string, rows: ReturnType<typeof installed>[], path: string | undefined = macPath) =>
    setUpPathOn({ installs: rows, projectId: "cp", path, daemonId, platformOf });

  it("does not send a Mac's folder to a Linux machine (2026-11-23: mkdir /Users: permission denied)", () => {
    const rows = [installed("mac1", macPath), { ...row("cp", "ovh", ProjectInstallState.FAILED), path: macPath }];
    expect(pathOn("ovh", rows)).toBeUndefined();
  });

  it("keeps the project's folder on a machine of the same platform", () => {
    expect(pathOn("mac2", [installed("mac1", macPath)])).toBe(macPath);
  });

  it("uses where a machine of the target's platform has the checkout", () => {
    expect(pathOn("ovh", [installed("mac1", macPath), installed("box", "/home/me/cp")])).toBe("/home/me/cp");
  });

  // The project's path is a folder on the machine it was added from. With
  // nothing to show it fits this one, the server's default for this machine
  // is the safe answer: it is always creatable there.
  it("lets the server choose when nothing shows the project's folder fits this machine", () => {
    expect(pathOn("ovh", [])).toBeUndefined();
    expect(pathOn("odd", [installed("mac1", macPath)])).toBeUndefined();
    expect(pathOn("ovh", [installed("odd", macPath)])).toBeUndefined();
    expect(pathOn("ovh", [], "  ")).toBeUndefined();
  });

  // "Try again" after the prod failure: the only record of the project is
  // the failed row itself, at the path that failed. A retry must not send it
  // again (it fails the same way, every time).
  it("never resends the folder a failed set-up could not create", () => {
    const failedOnly = [{ ...row("cp", "ovh", ProjectInstallState.FAILED), path: macPath }];
    expect(pathOn("ovh", failedOnly)).toBeUndefined();
  });
});

describe("sectionMachines", () => {
  it("puts the project's machines first (ready, then setting up), and the rest under not set up, keeping order", () => {
    const machines = ["cicd", "gpu", "laptop", "old"];
    const sections = sectionMachines(machines, (id) => id, projectPlacement(installs, "temp"));
    expect(sections.ready).toEqual(["laptop", "gpu"]);
    expect(sections.notSetUp).toEqual(["cicd", "old"]);
  });
});

describe("preferredDaemonIds", () => {
  it("narrows the default to installed machines, else to the ones being set up", () => {
    expect([...(preferredDaemonIds(installs, "temp") ?? [])]).toEqual(["laptop"]);
    const landing = [row("p", "gpu", ProjectInstallState.INSTALLING), row("p", "old", ProjectInstallState.FAILED)];
    expect([...(preferredDaemonIds(landing, "p") ?? [])]).toEqual(["gpu"]);
    expect(preferredDaemonIds([row("p", "old", ProjectInstallState.FAILED)], "p")).toBeUndefined();
    expect(preferredDaemonIds(installs, undefined)).toBeUndefined();
  });
});

describe("helpers", () => {
  it("only a repository can be set up on another machine", () => {
    expect(canSetUpElsewhere({ remote_url: "https://github.com/acme/temp" })).toBe(true);
    expect(canSetUpElsewhere({})).toBe(false);
    expect(canSetUpElsewhere(null)).toBe(false);
  });

  it("knows when a clone for the project is still landing", () => {
    expect(isSettingUpAnywhere(installs, "temp")).toBe(true);
    expect(isSettingUpAnywhere(installs, "other")).toBe(false);
    expect(isSettingUpAnywhere(installs, undefined)).toBe(false);
  });
});

describe("moveSourceDaemonIds", () => {
  const rows = [
    row("temp", "laptop", ProjectInstallState.INSTALLED),
    row("temp", "desk", ProjectInstallState.INSTALLED),
    row("temp", "gpu", ProjectInstallState.INSTALLING),
    row("temp", "old", ProjectInstallState.FAILED),
    row("other", "cicd", ProjectInstallState.INSTALLED),
  ];

  it("is every machine with the checkout but the target, an online one first", () => {
    expect(moveSourceDaemonIds(rows, "temp", "cicd", (id) => id === "desk")).toEqual(["desk", "laptop"]);
    expect(moveSourceDaemonIds(rows, "temp", "laptop")).toEqual(["desk"]);
  });

  it("is none for a project with no checkout anywhere else", () => {
    expect(moveSourceDaemonIds(rows, "other", "cicd")).toEqual([]);
    expect(moveSourceDaemonIds(rows, undefined, "cicd")).toEqual([]);
  });
});

describe("nested repositories", () => {
  it("maps the wire rows, and none from an older server", () => {
    expect(
      nestedRepoInstalls([{ name: "forge", relativePath: "forge", installState: ProjectInstallState.FAILED, installError: "denied" }]),
    ).toEqual([{ name: "forge", relative_path: "forge", install_state: ProjectInstallState.FAILED, install_error: "denied" }]);
    expect(nestedRepoInstalls(undefined)).toEqual([]);
  });

  it("labels a repository by its folder, naming it when the folder is called something else", () => {
    expect(nestedRepoLabel({ name: "forge", relative_path: "forge" })).toBe("forge/");
    expect(nestedRepoLabel({ name: "sdk", relative_path: "tools/sdk/" })).toBe("tools/sdk/");
    expect(nestedRepoLabel({ name: "sdk", relative_path: "vendor/client" })).toBe("sdk (vendor/client/)");
    expect(nestedRepoLabel({ name: "api", relative_path: "" })).toBe("api");
  });
});
