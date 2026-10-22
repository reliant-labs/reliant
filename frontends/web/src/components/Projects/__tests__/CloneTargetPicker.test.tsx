import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { CloneTargetPicker } from "../CloneTargetPicker";
import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";

function machine(id: string, name: string, status: DaemonStatus): DaemonInfo {
  return {
    daemonId: id,
    name,
    hostname: `ws-ws-${id.slice(0, 8)}`,
    daemonType: "managed",
    status,
    lastStatusMessage: "",
  } as unknown as DaemonInfo;
}

describe("CloneTargetPicker", () => {
  // The owner had exactly one machine listed and could not tell which machine
  // "Clone from GitHub" would use. With one candidate the dialog used to show
  // nothing at all; the target is now always stated.
  it("states the target when there is only one machine", () => {
    render(
      <CloneTargetPicker
        daemons={[machine("2aab1465-f76b", "default", DaemonStatus.ACTIVE)]}
        selectedDaemonId="2aab1465-f76b"
        onSelect={vi.fn()}
      />,
    );
    const summary = screen.getByTestId("clone-target-summary");
    expect(summary).toHaveTextContent("Clone onto");
    expect(summary).toHaveTextContent("default");
    expect(summary).toHaveTextContent(/clones now/i);
    expect(summary).not.toHaveTextContent("ws-ws-");
  });

  it("lets the user pick when there are several, by name", () => {
    render(
      <CloneTargetPicker
        daemons={[
          machine("aaaaaaaa-1", "default", DaemonStatus.ACTIVE),
          machine("bbbbbbbb-2", "gpu-box", DaemonStatus.SUSPENDED),
        ]}
        selectedDaemonId="aaaaaaaa-1"
        onSelect={vi.fn()}
      />,
    );
    const group = screen.getByRole("radiogroup", { name: /machine to clone onto/i });
    expect(group).toHaveTextContent("default");
    expect(group).toHaveTextContent("gpu-box");
    expect(screen.getByTestId("clone-target-aaaaaaaa-1")).toHaveAttribute("aria-checked", "true");
  });

  it("offers nothing to pick when no machine can take a clone", () => {
    const { container } = render(
      <CloneTargetPicker
        daemons={[machine("cccccccc-3", "broken", DaemonStatus.FAILED)]}
        selectedDaemonId={null}
        onSelect={vi.fn()}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
