// Copyright (c) 2025 Reliant Labs
/**
 * The machine picker, given the new chat's project, never offers a machine
 * the chat would fail on. The owner's report: pick a project, pick a machine,
 * and only then hear "temp isn't on cicd". Now the project's machines come
 * first, marked ready with their online state; every other machine sits under
 * "Not set up for temp", is not a choice as it is, and offers "Set up temp on
 * cicd" — which clones it there and then chooses it.
 */
import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { DEFAULT_MACHINE } from "@/lib/chatMachine";
import { projectPlacement } from "@/lib/projectPlacement";
import { MachinePicker, type MachinePickerProject } from "../MachinePicker";

vi.mock("../../forge-ui/tooltip", () => ({ default: ({ children }: { children: React.ReactNode }) => <>{children}</> }));

const laptop = { daemonId: "d-laptop", hostname: "laptop", status: DaemonStatus.DISCONNECTED };
const cicd = { daemonId: "d-cicd", hostname: "cicd", status: DaemonStatus.ACTIVE };
const gpu = { daemonId: "d-gpu", hostname: "gpu", status: DaemonStatus.SUSPENDED };
const daemons = [laptop, cicd, gpu];

const installs = [
  { project_id: "temp", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED },
  { project_id: "temp", daemon_id: "d-gpu", install_state: ProjectInstallState.INSTALLING },
];

function project(overrides: Partial<MachinePickerProject> = {}): MachinePickerProject {
  return { name: "temp", placement: projectPlacement(installs, "temp"), onSetUp: vi.fn(async () => {}), ...overrides };
}

function open(props: Partial<React.ComponentProps<typeof MachinePicker>> = {}) {
  const onChange = vi.fn();
  render(
    <MachinePicker
      value={DEFAULT_MACHINE}
      onChange={onChange}
      daemons={daemons}
      defaultDaemon={laptop}
      project={project()}
      {...props}
    />,
  );
  fireEvent.click(screen.getByTestId("machine-picker"));
  return { onChange };
}

describe("MachinePicker: where the project is set up", () => {
  it("lists the project's machines first, marked ready (or setting up), with their online state", () => {
    open();
    const ready = screen.getByRole("group", { name: "Set up for temp" });
    const rows = within(ready).getAllByRole("option");
    // Ready before setting up, whatever the machines' own status order.
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining("laptop"),
      expect.stringContaining("gpu"),
    ]);
    expect(rows[0]).toHaveTextContent("Ready·offline");
    expect(rows[0]).toHaveAttribute("aria-selected", "true");
    expect(rows[1]).toHaveTextContent("Setting up…·suspended");
  });

  it("puts every other machine under Not set up, where it is not a choice", () => {
    const { onChange } = open();
    const notSetUp = screen.getByRole("group", { name: "Not set up for temp" });
    const row = within(notSetUp).getByRole("option");
    expect(row).toHaveTextContent("cicd");
    expect(row).toHaveTextContent("Not set up·online");
    expect(row).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(row);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("sets the project up on that machine, then chooses it", async () => {
    const onSetUp = vi.fn(async () => {});
    const { onChange } = open({ project: project({ onSetUp }) });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Set up temp on cicd" }));
    });
    expect(onSetUp).toHaveBeenCalledWith("d-cicd");
    expect(onChange).toHaveBeenCalledWith("d-cicd");
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("says why when the set-up is refused, and keeps the machine unchosen", async () => {
    const onSetUp = vi.fn(async () => {
      throw new Error("no git credential found for provider github");
    });
    const { onChange } = open({
      project: project({ onSetUp, describeError: (err) => `Couldn't clone: ${(err as Error).message}` }),
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Set up temp on cicd" }));
    });
    expect(screen.getByRole("alert")).toHaveTextContent("Couldn't clone: no git credential found for provider github");
    expect(onChange).not.toHaveBeenCalled();
  });

  it("offers Try again on a machine whose clone failed", () => {
    const failed = [...installs, { project_id: "temp", daemon_id: "d-cicd", install_state: ProjectInstallState.FAILED }];
    open({ project: project({ placement: projectPlacement(failed, "temp") }) });
    const row = screen.getByTestId("machine-not-set-up-d-cicd");
    expect(row).toHaveTextContent("Set-up failed");
    expect(within(row).getByRole("button", { name: "Try setting up temp on cicd again" })).toBeInTheDocument();
  });

  it("explains, without an action, when the project is a folder that cannot be cloned", () => {
    open({ project: project({ onSetUp: undefined }) });
    expect(screen.getByText("temp is a folder on another machine, so it can't be cloned here.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Set up temp/ })).toBeNull();
  });

  it("is one flat list when the project has no install record anywhere", () => {
    const { onChange } = open({ project: project({ placement: projectPlacement(installs, "fresh") }) });
    expect(screen.queryByRole("group")).toBeNull();
    fireEvent.click(screen.getByRole("option", { name: /cicd/ }));
    expect(onChange).toHaveBeenCalledWith("d-cicd");
  });
});
