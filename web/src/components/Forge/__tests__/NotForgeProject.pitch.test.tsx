// Copyright (c) 2025 Reliant Labs

/**
 * The not-a-forge-project state: the fact first, then the case for forge.
 *
 * Most reliant projects have no forge.yaml, so this is the screen most people
 * see the first time they open Deployments. It used to say only "this is
 * expected" — accurate, and a dead end. It now states the same fact and then
 * says what forge would give this project and how to start.
 *
 * Pinned here: the fact survives, the pitch is scannable (a heading and a
 * short list, not a paragraph), the call to action names ways in that actually
 * exist, and the copy makes no numeric claims.
 */

import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";

import { NotForgeProject } from "../ForgeStates";

describe("NotForgeProject", () => {
  it("keeps the factual line: the named project has no forge.yaml", () => {
    render(<NotForgeProject projectName="acme-api" />);
    const fact = screen.getByTestId("forge-not-project-fact");
    expect(fact.textContent).toContain("acme-api");
    expect(fact.textContent).toContain("forge.yaml");
  });

  it("falls back to 'This project' when the name is unknown", () => {
    render(<NotForgeProject />);
    expect(screen.getByTestId("forge-not-project-fact").textContent).toMatch(/^This project has no/);
  });

  it("pitches forge under one heading with three scannable points", () => {
    render(<NotForgeProject projectName="acme-api" />);
    expect(screen.getByRole("heading", { level: 2, name: "Ship this project with forge" })).toBeInTheDocument();

    const list = screen.getByRole("list");
    expect(within(list).getAllByRole("listitem")).toHaveLength(3);
    expect(within(list).getAllByRole("heading", { level: 3 }).map((h) => h.textContent)).toEqual([
      "Deploy without the yak-shaving",
      "Fewer tokens, less guesswork",
      "Best practices, enforced",
    ]);
  });

  it("names ways in that exist: the Forge Migrate workflow and forge project new", () => {
    render(<NotForgeProject projectName="acme-api" />);
    const cta = screen.getByTestId("forge-not-project-cta");
    // getWorkflowDisplayName("builtin://forge-migrate", true) — what the
    // workflow selector actually shows.
    expect(cta.textContent).toContain("Forge Migrate");
    expect(cta.textContent).toContain("forge project new");
    // There is no `forge init`; naming it would send people to an error.
    expect(cta.textContent).not.toContain("forge init");
  });

  it("makes no numeric claims", () => {
    render(<NotForgeProject />);
    expect(screen.getByTestId("forge-not-project").textContent).not.toMatch(/\d/);
  });

  it("reads as information, not an error, and builds no structure from bg-muted", () => {
    render(<NotForgeProject projectName="acme-api" />);
    const panel = screen.getByTestId("forge-not-project");
    expect(panel.className).not.toMatch(/destructive/);
    expect(panel.outerHTML).not.toMatch(/bg-muted/);
  });
});
