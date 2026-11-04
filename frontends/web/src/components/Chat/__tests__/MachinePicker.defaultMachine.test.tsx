// Copyright (c) 2025 Reliant Labs
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { DEFAULT_MACHINE } from "@/lib/chatMachine";
import { MachinePicker } from "../MachinePicker";

const daemons = [
  { daemonId: "d-cicd", hostname: "cicd", status: DaemonStatus.ACTIVE },
  { daemonId: "d-default", hostname: "default", status: DaemonStatus.SUSPENDED, isDefault: true },
];

describe("MachinePicker: the default machine", () => {
  it("marks the default and offers Make default on every other machine", () => {
    const onMakeDefault = vi.fn();
    const onChange = vi.fn();
    render(
      <MachinePicker
        value={DEFAULT_MACHINE}
        onChange={onChange}
        daemons={daemons}
        defaultDaemon={daemons[1]}
        onMakeDefault={onMakeDefault}
      />,
    );
    expect(screen.getByTestId("machine-picker")).toHaveAccessibleName("Runs on: default");
    fireEvent.click(screen.getByTestId("machine-picker"));

    expect(screen.getByRole("option", { name: /default.*Default/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByRole("button", { name: "Make default your default machine" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Make cicd your default machine" }));
    expect(onMakeDefault).toHaveBeenCalledWith("d-cicd");
    expect(onChange).not.toHaveBeenCalled();
  });

  it("offers nothing when the caller cannot set a default", () => {
    render(<MachinePicker value={DEFAULT_MACHINE} onChange={vi.fn()} daemons={daemons} />);
    fireEvent.click(screen.getByTestId("machine-picker"));
    expect(screen.queryByRole("button", { name: /your default machine/ })).toBeNull();
  });
});
