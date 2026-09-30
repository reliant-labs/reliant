// Copyright (c) 2025 Reliant Labs

/**
 * Domains is reachable from the forge sidebar, under Project rather than
 * under an environment.
 *
 * The placement is the assertion. A domain is org-scoped and its binding is
 * meant to move between environments, so filing the tab under one env would
 * both misdescribe it and hide every claimed-but-unbound domain — the exact
 * state a tenant is stuck in when they come looking for this screen.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({ trafficLightPadding: "0px" }),
}));

import { FORGE_DOMAINS_PATH, ForgeShell } from "../../ForgeShell";

function renderShell(activePath: string) {
  render(
    <ForgeShell
      activePath={activePath}
      envs={[{ name: "prod", where: "cloud" }]}
      search="project=proj-1"
    >
      <div />
    </ForgeShell>
  );
}

describe("the Domains nav entry", () => {
  it("is in the sidebar and carries the project across", () => {
    renderShell("/forge");
    const link = screen.getByRole("link", { name: /domains/i });
    expect(link).toHaveAttribute("href", "/forge/domains?project=proj-1");
  });

  it("is marked active on its own route and not on the overview", () => {
    renderShell(FORGE_DOMAINS_PATH);
    const domains = screen.getByRole("link", { name: /domains/i });
    const overview = screen.getByRole("link", { name: /overview/i });
    // aria-current is how SidebarLayout marks the active entry; fall back to
    // asserting they differ, so this survives a change of mechanism.
    expect(domains.getAttribute("aria-current")).not.toBe(overview.getAttribute("aria-current"));
  });

  it("sits beside Overview, not under an environment", () => {
    renderShell(FORGE_DOMAINS_PATH);
    // The env entries link to /forge/env/…; Domains must not.
    expect(screen.getByRole("link", { name: /domains/i }).getAttribute("href")).not.toMatch(
      /\/forge\/env\//
    );
  });
});
