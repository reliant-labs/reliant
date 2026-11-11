/**
 * The sidebar's machine filter: All machines, or any set of machines, each
 * row a checkbox with an "Only" shortcut. Choosing reports the new selection
 * and nothing else; the sidebar persists it.
 */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { NO_MACHINE } from "../../../lib/sidebarMachineFilter";
import { MachineFilterMenu } from "../MachineFilterMenu";

const machines = [
  { daemonId: "laptop", hostname: "laptop", name: "Laptop", status: DaemonStatus.SUSPENDED, daemonType: "self_hosted" },
  { daemonId: "cloud", hostname: "cloud", name: "Cloud box", status: DaemonStatus.ACTIVE, daemonType: "managed" },
];

function renderMenu(filter: readonly string[] | null, extra: Partial<Parameters<typeof MachineFilterMenu>[0]> = {}) {
  const props = { filter, machines, onChange: vi.fn(), ...extra };
  render(<MachineFilterMenu {...props} />);
  return props;
}

async function open() {
  await userEvent.click(screen.getByTestId("machine-filter-trigger"));
}

describe("MachineFilterMenu", () => {
  it("is an icon at rest with All machines, and names a filter when one is on", () => {
    renderMenu(null);
    expect(screen.getByTestId("machine-filter-trigger")).toHaveAccessibleName("Filter by machine. Showing All machines");
    expect(screen.queryByTestId("machine-filter-label")).toBeNull();
  });

  it("names the one machine it shows, or how many", () => {
    const { unmount } = render(<MachineFilterMenu filter={["cloud"]} machines={machines} onChange={vi.fn()} />);
    expect(screen.getByTestId("machine-filter-label")).toHaveTextContent("Cloud box");
    unmount();
    renderMenu(["cloud", "laptop"]);
    expect(screen.getByTestId("machine-filter-label")).toHaveTextContent("2 machines");
  });

  it("lists every machine checked under All machines, online first", async () => {
    renderMenu(null);
    await open();
    const boxes = screen.getAllByRole("checkbox");
    expect(boxes.map((box) => box.closest("label")?.textContent)).toEqual([
      expect.stringContaining("Cloud box"),
      expect.stringContaining("Laptop"),
    ]);
    expect(boxes.every((box) => (box as HTMLInputElement).checked)).toBe(true);
    expect(screen.getByTestId("machine-filter-all")).toHaveAttribute("aria-pressed", "true");
  });

  it("unchecking a machine shows the others", async () => {
    const props = renderMenu(null);
    await open();
    await userEvent.click(screen.getByTestId("machine-filter-cloud"));
    expect(props.onChange).toHaveBeenCalledWith(["laptop"]);
  });

  it("checking the last hidden machine is All machines again", async () => {
    const props = renderMenu(["laptop"]);
    await open();
    expect(screen.getByTestId("machine-filter-cloud")).not.toBeChecked();
    await userEvent.click(screen.getByTestId("machine-filter-cloud"));
    expect(props.onChange).toHaveBeenCalledWith(null);
  });

  it("keeps the last machine shown from being turned off", async () => {
    const props = renderMenu(["laptop"]);
    await open();
    expect(screen.getByTestId("machine-filter-laptop")).toBeDisabled();
    await userEvent.click(screen.getByTestId("machine-filter-laptop"));
    expect(props.onChange).not.toHaveBeenCalled();
  });

  it("shows one machine alone with Only, and everything with All machines", async () => {
    const props = renderMenu(null);
    await open();
    await userEvent.click(screen.getByRole("button", { name: "Show only Laptop" }));
    expect(props.onChange).toHaveBeenLastCalledWith(["laptop"]);
    await userEvent.click(screen.getByTestId("machine-filter-all"));
    expect(props.onChange).toHaveBeenLastCalledWith(null);
  });

  it("offers No machine only when asked to", async () => {
    renderMenu(null, { includeNoMachine: true, needsYouByMachine: new Map([[NO_MACHINE, 2]]) });
    await open();
    const noMachine = screen.getByTestId(`machine-filter-${NO_MACHINE}`);
    expect(noMachine.closest("label")).toHaveTextContent("No machine2");
  });

  it("shows how many chats wait on the user per machine", async () => {
    renderMenu(null, { needsYouByMachine: new Map([["laptop", 3]]) });
    await open();
    expect(screen.getByTestId("machine-filter-laptop").closest("label")).toHaveTextContent("Laptop3");
    expect(screen.queryByTestId(`machine-filter-${NO_MACHINE}`)).toBeNull();
  });
});
