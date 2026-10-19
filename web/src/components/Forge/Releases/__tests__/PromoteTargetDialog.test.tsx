// Copyright (c) 2025 Reliant Labs

/** The promote picker never confirms an environment that already runs the release. */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { PromoteTargetDialog } from "../releaseParts";
import { RELEASE_ENVS } from "../__fixtures__/releaseFixtures";

const LONG = RELEASE_ENVS.filter((env) => env.kind === "long");

describe("PromoteTargetDialog", () => {
  it("disables confirm when every environment already runs the release", async () => {
    const onConfirm = vi.fn();
    render(
      <PromoteTargetDialog version="v1" envs={LONG} bases={new Map(LONG.map((env) => [env.name, "v1"]))} onCancel={vi.fn()} onConfirm={onConfirm} />
    );
    const confirm = screen.getByTestId("promote-target-confirm");
    expect(confirm).toBeDisabled();
    expect(confirm).toHaveTextContent("Promote…");
    expect(screen.getByTestId("promote-target-everywhere")).toBeInTheDocument();
    for (const radio of screen.getAllByRole("radio")) expect(radio).not.toBeChecked();
    await userEvent.setup().click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("starts on the first environment that does not run it, and names it", async () => {
    const onConfirm = vi.fn();
    render(
      <PromoteTargetDialog
        version="v2"
        envs={LONG}
        bases={new Map([["prod", "v2"], ["e2e", "v1"], ["dev", "v2"]])}
        initialEnv="prod"
        onCancel={vi.fn()}
        onConfirm={onConfirm}
      />
    );
    await userEvent.setup().click(screen.getByTestId("promote-target-confirm"));
    expect(onConfirm).toHaveBeenCalledWith("e2e");
  });
});
