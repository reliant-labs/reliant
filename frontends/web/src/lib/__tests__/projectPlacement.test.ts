import { describe, expect, it } from "vitest";

import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import {
  canChoose,
  canSetUpElsewhere,
  canStartOn,
  isSettingUpAnywhere,
  preferredDaemonIds,
  projectPlacement,
  sectionMachines,
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
    expect(reason("cicd")).toBe("temp isn't set up on cicd. Set it up there, or pick a machine it's on.");
    expect(reason("gpu")).toBe("Setting up temp on gpu. You can send once it's there.");
    expect(reason("old")).toBe("Setting up temp on old failed. Try again, or pick a machine it's on.");
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
