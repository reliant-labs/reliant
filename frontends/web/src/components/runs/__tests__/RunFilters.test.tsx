// Copyright (c) 2025 Reliant Labs

/**
 * The Runs filter bar is bound to the URL: picking from a filter's menu writes
 * the search param, the button says what is applied, and Clear removes it all.
 */

import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { RunFilters } from "../RunFilters";
import { renderRunsAt } from "./runTestUtils";

function renderFilters(path = "/workflows/runs") {
  return renderRunsAt(
    <RunFilters currentProjectName="Reliant" triggerName={undefined} />,
    path,
  );
}

/** Open a filter's menu by the label its button starts with. */
async function openFilter(user: ReturnType<typeof userEvent.setup>, label: string) {
  await user.click(await screen.findByRole("button", { name: new RegExp(`^${label}:`) }));
  return screen.findByRole("menu", { name: label });
}

describe("RunFilters", () => {
  it("is one row of menus, not rows of chips", async () => {
    renderFilters();
    expect(await screen.findByRole("button", { name: "Project: Reliant" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Started: Last 24 hours" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "State: Any" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Started by: Anyone" })).toBeInTheDocument();
    // No option is a standalone button any more.
    expect(screen.queryByRole("button", { name: "Failed" })).toBeNull();
  });

  it("the State menu toggles values into the URL, and the button says them", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters();

    const menu = await openFilter(user, "State");
    const failed = within(menu).getByRole("menuitemcheckbox", { name: "Failed" });
    expect(failed).toHaveAttribute("aria-checked", "false");
    await user.click(failed);
    await waitFor(() => expect(router.state.location.search).toMatchObject({ state: ["failed"] }));
    expect(screen.getByRole("button", { name: "State: Failed" })).toBeInTheDocument();

    // The menu stays open for a second pick; unpicking drops the param.
    await user.click(within(screen.getByRole("menu", { name: "State" })).getByRole("menuitemcheckbox", { name: "Failed" }));
    await waitFor(() => expect((router.state.location.search as { state?: string[] }).state).toBeUndefined());
  });

  it("Started by writes the launch kind; Tests filters to builder test runs", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters();
    const menu = await openFilter(user, "Started by");
    await user.click(within(menu).getByRole("menuitemcheckbox", { name: "Agent" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ kind: ["agent.start_run"] }));
    await user.click(within(screen.getByRole("menu", { name: "Started by" })).getByRole("menuitemcheckbox", { name: "Tests" }));
    await waitFor(() =>
      expect(router.state.location.search).toMatchObject({ kind: ["agent.start_run", "builder.test"] }),
    );
  });

  it("defaults to the current project; All projects widens it", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters();
    const menu = await openFilter(user, "Project");
    expect(within(menu).getByRole("menuitemradio", { name: "Reliant" })).toHaveAttribute("aria-checked", "true");
    await user.click(within(menu).getByRole("menuitemradio", { name: "All projects" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ allProjects: true }));
  });

  it("the Started menu writes the range; the default is absent from the URL", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters();
    await user.click(within(await openFilter(user, "Started")).getByRole("menuitemradio", { name: "Last 30 days" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ range: "30d" }));
    await user.click(within(await openFilter(user, "Started")).getByRole("menuitemradio", { name: "Last 24 hours" }));
    await waitFor(() => expect((router.state.location.search as { range?: string }).range).toBeUndefined());
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

  it("Group repeats writes group=false when turned off", async () => {
    const user = userEvent.setup();
    const { router } = renderFilters();
    await user.click(await screen.findByRole("checkbox", { name: "Group repeats" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ group: false }));
  });
});
