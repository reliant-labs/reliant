// Copyright (c) 2025 Reliant Labs
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";

const track = vi.fn();
vi.mock("@/lib/analytics", () => ({ trackEvent: (...a: unknown[]) => track(...a) }));

import {
  CLAIMABLE_DEPLOY_ADOPTS,
  CLAUDE_MCP_COMMAND,
  claimableDeployCommands,
  ForgeFirstRun,
  signedInDeployCommands,
} from "../ForgeFirstRun";

const writeText = vi.fn(async (_: string) => undefined);

beforeEach(() => {
  vi.clearAllMocks();
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
});

describe("the forge first-run steps", () => {
  it("replaces the dead-end empty state with three steps", () => {
    render(<ForgeFirstRun />);
    expect(screen.getByTestId("apps-empty")).toBeInTheDocument();
    expect(screen.queryByText("Nothing deployed yet")).toBeNull();
    expect(screen.getByTestId("forge-first-run-step-1")).toHaveTextContent("In your app folder, deploy");
    expect(screen.getByTestId("forge-first-run-step-2")).toHaveTextContent("Or ask your agent");
    expect(screen.getByTestId("forge-first-run-step-3")).toHaveTextContent("Read the docs");
  });

  it("shows the signed-in first deploy with npx, so no install is needed", () => {
    expect(signedInDeployCommands().map((c) => c.command)).toEqual([
      "npx -y @reliantlabs/forge login",
      "npx -y @reliantlabs/forge project new --in-place --kind site",
      "npx -y @reliantlabs/forge env deploy prod",
    ]);
    render(<ForgeFirstRun />);
    const step = within(screen.getByTestId("forge-first-run-step-1"));
    expect(step.getAllByText("npx -y @reliantlabs/forge login")).toHaveLength(1);
    expect(step.getAllByText("npx -y @reliantlabs/forge env deploy prod")).toHaveLength(1);
  });

  it("shows the two-command no-login deploy while forge#2163 is unreleased, one command after", () => {
    expect(CLAIMABLE_DEPLOY_ADOPTS).toBe(false);
    expect(claimableDeployCommands().map((c) => c.command)).toEqual([
      "npx -y @reliantlabs/forge project new --in-place --kind site",
      "npx -y @reliantlabs/forge env deploy prod --claimable",
    ]);
    expect(claimableDeployCommands(true).map((c) => c.command)).toEqual(["npx -y @reliantlabs/forge env deploy prod --claimable"]);
  });

  it("offers the MCP install command and the Cursor and VS Code links", () => {
    render(<ForgeFirstRun />);
    expect(screen.getByText(CLAUDE_MCP_COMMAND)).toBeInTheDocument();
    expect(CLAUDE_MCP_COMMAND).toBe("claude mcp add forge -- npx -y @reliantlabs/forge mcp");
    expect(screen.getByTestId("forge-first-run-cursor").getAttribute("href")).toMatch(/^https:\/\/cursor\.com\/en\/install-mcp\?name=forge&config=/);
    expect(screen.getByTestId("forge-first-run-vscode").getAttribute("href")).toMatch(/^https:\/\/insiders\.vscode\.dev\/redirect\/mcp\/install\?name=forge/);
  });

  it("links to the docs", () => {
    render(<ForgeFirstRun />);
    expect(screen.getByTestId("forge-first-run-docs")).toHaveAttribute("href", "https://docs.reliantlabs.io/forge/quick-start");
  });

  it("every command has a copy button that copies it and records an opaque id, never the command", async () => {
    render(<ForgeFirstRun />);
    const copy = screen.getByRole("button", { name: `Copy ${CLAUDE_MCP_COMMAND}` });
    fireEvent.click(copy);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(CLAUDE_MCP_COMMAND));
    expect(track).toHaveBeenCalledWith("forge_first_run_copied", { id: "claude_mcp" });

    fireEvent.click(screen.getByRole("button", { name: "Copy npx -y @reliantlabs/forge env deploy prod" }));
    await waitFor(() => expect(track).toHaveBeenCalledWith("forge_first_run_copied", { id: "deploy" }));

    // Only opaque ids: lowercase words and underscores, never a command line.
    for (const [, props] of track.mock.calls) {
      for (const v of Object.values(props as Record<string, unknown>)) expect(v).toMatch(/^[a-z_]+$/);
    }
  });

  it("puts a copy button on every command: 3 signed-in, 2 no-login, 1 MCP", () => {
    render(<ForgeFirstRun />);
    expect(screen.getAllByRole("button", { name: /^Copy / })).toHaveLength(6);
  });

  it("records link clicks by id", () => {
    render(<ForgeFirstRun />);
    for (const [id, tid] of [["cursor_mcp", "forge-first-run-cursor"], ["vscode_mcp", "forge-first-run-vscode"], ["docs", "forge-first-run-docs"]] as const) {
      const link = screen.getByTestId(tid);
      link.addEventListener("click", (e) => e.preventDefault());
      fireEvent.click(link);
      expect(track).toHaveBeenCalledWith("forge_first_run_opened", { id });
    }
  });
});
