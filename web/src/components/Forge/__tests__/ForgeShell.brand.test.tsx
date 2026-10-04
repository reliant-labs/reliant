// Copyright (c) 2025 Reliant Labs

/**
 * The shell's brand reads "Deployments" — the user-facing name of this
 * surface — while everything underneath it is still forge: the routes, the
 * component names and the page copy that talks about forge commands. The
 * rename is navigation copy only, so the routes are pinned alongside it.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({ trafficLightPadding: "0px" }),
}));

import { FORGE_OVERVIEW_PATH, ForgeShell } from "../ForgeShell";

describe("the forge shell brand", () => {
  it("reads Deployments, not forge", () => {
    render(
      <ForgeShell activePath={FORGE_OVERVIEW_PATH}>
        <div />
      </ForgeShell>
    );
    expect(screen.getByText("Deployments")).toBeInTheDocument();
    expect(screen.queryByText("forge")).toBeNull();
  });

  it("keeps the /forge routes", () => {
    render(
      <ForgeShell activePath={FORGE_OVERVIEW_PATH}>
        <div />
      </ForgeShell>
    );
    expect(screen.getByRole("link", { name: /overview/i })).toHaveAttribute("href", "/forge");
  });
});
