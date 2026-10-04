// Copyright (c) 2025 Reliant Labs

/**
 * Filter chips are bound to the URL: pressing one writes the search param,
 * the active value shows on the chip, and Clear removes them all.
 */

import { describe, expect, it } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { RunFilters } from "../RunFilters";
import { renderRunsAt } from "./runTestUtils";

function renderFilters(path = "/workflows/runs") {
  return renderRunsAt(
    <RunFilters currentProjectName="Reliant" triggerName={undefined} />,
    path,
  );
}

describe("RunFilters", () => {
  it("a state chip toggles its value into the URL", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters();

    const failed = await screen.findByRole("button", { name: "Failed" });
    expect(failed).toHaveAttribute("aria-pressed", "false");
    await user.click(failed);

    await waitFor(() => expect(router.state.location.search).toMatchObject({ state: ["failed"] }));
    expect(screen.getByRole("button", { name: "Failed" })).toHaveAttribute("aria-pressed", "true");

    await user.click(screen.getByRole("button", { name: "Failed" }));
    await waitFor(() => expect((router.state.location.search as { state?: string[] }).state).toBeUndefined());
  });

  it("a kind chip writes the launch kind", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters();
    await user.click(await screen.findByRole("button", { name: "Agent" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ kind: ["agent.start_run"] }));
  });

  it("the Tests chip filters to builder test runs and toggles back off", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters();
    const tests = await screen.findByRole("button", { name: "Tests" });
    expect(tests).toHaveAttribute("aria-pressed", "false");

    await user.click(tests);
    await waitFor(() => expect(router.state.location.search).toMatchObject({ kind: ["builder.test"] }));
    expect(screen.getByRole("button", { name: "Tests" })).toHaveAttribute("aria-pressed", "true");

    await user.click(screen.getByRole("button", { name: "Tests" }));
    await waitFor(() => expect((router.state.location.search as { kind?: string[] }).kind).toBeUndefined());
  });

  it("defaults to the current project; the All projects chip widens it", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters();
    const allProjects = await screen.findByRole("button", { name: "All projects" });
    expect(allProjects).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByText(/Reliant/)).toBeInTheDocument();
    await user.click(allProjects);
    await waitFor(() => expect(router.state.location.search).toMatchObject({ allProjects: true }));
  });

  it("the time chip writes the range", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters();
    await user.click(await screen.findByRole("button", { name: "30 days" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ range: "30d" }));
  });

  it("an automation filter shows its name and can be removed", async () => {
    const user = userEvent.setup();
    const { router } = renderRunsAt(
      <RunFilters currentProjectName="Reliant" triggerName="Hourly sweep" />,
      "/workflows/runs?trigger=trig-1",
    );
    await user.click(await screen.findByRole("button", { name: "Remove filter Automation: Hourly sweep" }));
    await waitFor(() => expect((router.state.location.search as { trigger?: string }).trigger).toBeUndefined());
  });

  it("Clear removes every filter", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters('/workflows/runs?state=%5B%22failed%22%5D&kind=%5B%22schedule%22%5D&range=%227d%22');
    await user.click(await screen.findByRole("button", { name: "Clear filters" }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
  });
});
