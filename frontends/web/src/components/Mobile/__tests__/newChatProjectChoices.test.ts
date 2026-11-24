// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import type { Project } from "@/hooks/project-queries";

import { newChatProjectChoices } from "../newChatProjectChoices";

const project = (id: string, over: Partial<Project> = {}): Project =>
  ({ id, name: id, path: `/p/${id}`, remote_url: `https://github.com/o/${id}`, is_git_repo: true, is_forge: false, last_active: "", created_at: "", updated_at: "", ...over }) as Project;
const laptop = { daemonId: "d1", hostname: "laptop", status: DaemonStatus.ACTIVE };
const cloud = { daemonId: "d2", hostname: "cloud", status: DaemonStatus.SUSPENDED };
const install = (project_id: string, daemon_id: string, install_state = ProjectInstallState.INSTALLED) => ({ project_id, daemon_id, install_state });

describe("newChatProjectChoices", () => {
  it("puts the current project first, then ready, unrecorded, set-up and unavailable ones", () => {
    const choices = newChatProjectChoices({
      projects: [project("folder", { remote_url: undefined }), project("away"), project("legacy"), project("ready"), project("current")],
      installs: [install("current", "d1"), install("ready", "d2"), install("away", "d9"), install("folder", "d9")],
      daemons: [laptop, cloud],
      currentProjectId: "current",
      setUpTarget: { daemonId: "d1", name: "laptop" },
    });
    expect(choices.map((c) => [c.project.id, c.kind])).toEqual([
      ["current", "ready"],
      ["ready", "ready"],
      ["legacy", "unrecorded"],
      ["away", "set-up"],
      ["folder", "unavailable"],
    ]);
    // An asleep machine counts: a send wakes it.
    expect(choices[1]).toMatchObject({ description: "On cloud", machineId: "d2" });
    expect(choices[3].description).toBe("Not on a connected machine · Set up on laptop");
  });

  it("names a clone still landing, and several machines in short", () => {
    const choices = newChatProjectChoices({
      projects: [project("a"), project("b")],
      installs: [install("a", "d1", ProjectInstallState.INSTALLING), install("b", "d1"), install("b", "d2"), install("b", "d3")],
      daemons: [laptop, cloud, { daemonId: "d3", hostname: "box", status: DaemonStatus.IDLE }],
      currentProjectId: undefined,
    });
    expect(choices.map((c) => c.description)).toEqual(["Setting up on laptop…", "On laptop and 2 more"]);
  });

  it("cannot set a project up with no machine to put it on", () => {
    const [choice] = newChatProjectChoices({ projects: [project("x")], installs: [install("x", "d9")], daemons: [], currentProjectId: undefined });
    expect(choice).toMatchObject({ kind: "unavailable", description: "Not on a connected machine · no machine to set it up on" });
  });
});
