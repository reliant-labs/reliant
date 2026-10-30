// Copyright (c) 2025 Reliant Labs

/** A platform dashboard is not per environment, and the top bar must not imply it is. */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { EnvScopeControl } from "../EnvScopeControl";
import { PLATFORM_SCOPE_LABEL } from "../envScope";

const LONG = [{ name: "prod", recorded: true }, { name: "dev", recorded: true }];

describe("EnvScopeControl", () => {
  it("offers the environments by default", async () => {
    const onChange = vi.fn();
    render(<EnvScopeControl value="prod" long={LONG} previews={[]} onChange={onChange} />);
    await userEvent.setup().click(screen.getByTestId("env-scope-dev"));
    expect(onChange).toHaveBeenCalledWith("dev");
    expect(screen.queryByTestId("env-scope-platform")).not.toBeInTheDocument();
  });

  it("says platform-wide, and offers no environment, on a platform dashboard", () => {
    render(<EnvScopeControl value="prod" long={LONG} previews={[]} onChange={vi.fn()} platformWide />);
    expect(screen.getByTestId("env-scope-platform")).toHaveTextContent("platform-wide (not per environment)");
    expect(PLATFORM_SCOPE_LABEL).toBe("platform-wide (not per environment)");
    expect(screen.queryByTestId("env-scope")).not.toBeInTheDocument();
    expect(screen.queryByTestId("env-scope-select")).not.toBeInTheDocument();
    expect(screen.queryByTestId("env-scope-dev")).not.toBeInTheDocument();
  });
});
