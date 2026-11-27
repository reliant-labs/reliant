// Copyright (c) 2026 Reliant Labs

/**
 * The suggested workflow: "Create this workflow" copies the description and
 * opens the workflow builder's New workflow (it takes no description to
 * prefill), creating nothing itself; the guide opens in a new tab.
 */

import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { AUTOMATE_GUIDE_URL, SuggestedWorkflow, WORKFLOW_COPIED, WORKFLOW_COPY_FAILED } from "../SuggestedWorkflow";
import { renderGtm } from "./gtmHarness";

const toasts = vi.hoisted(() => ({ success: vi.fn(), warning: vi.fn(), error: vi.fn() }));
vi.mock("@/lib/toast-manager", () => ({ toast: toasts }));

const writeText = vi.fn(async () => {});
Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("SuggestedWorkflow", () => {
  it("copies the description and opens New workflow, calling no GTM write", async () => {
    const { router } = renderGtm(<SuggestedWorkflow text={"Every Monday, draft a recap of last week's posts."} />, {}, { path: "/gtm/ideas/i-1", routePath: "/gtm/ideas/$ideaId" });
    fireEvent.click(await screen.findByRole("button", { name: "Create this workflow" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("Every Monday, draft a recap of last week's posts."));
    expect(toasts.success).toHaveBeenCalledWith(WORKFLOW_COPIED);
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflow/new"));
  });

  it("stays when the copy fails: the text is selected, with a link to New workflow", async () => {
    writeText.mockRejectedValueOnce(new Error("denied"));
    const { router } = renderGtm(<SuggestedWorkflow text="Draft a weekly recap." />, {}, { path: "/gtm/ideas/i-1", routePath: "/gtm/ideas/$ideaId" });
    fireEvent.click(await screen.findByRole("button", { name: "Create this workflow" }));
    await waitFor(() => expect(toasts.warning).toHaveBeenCalledWith(WORKFLOW_COPY_FAILED));
    expect(toasts.success).not.toHaveBeenCalled();
    expect(router.state.location.pathname).toBe("/gtm/ideas/i-1");
    // The text is selected (its whole node) and focused, ready to copy by hand.
    expect(window.getSelection()?.getRangeAt(0).toString()).toBe("Draft a weekly recap.");
    expect(screen.getByTestId("gtm-suggested-workflow-text")).toHaveFocus();
    expect(screen.getByRole("link", { name: "Open New workflow" })).toHaveAttribute("href", "/workflow/new");
  });

  it("links the guide in a new tab, without an opener", async () => {
    renderGtm(<SuggestedWorkflow text="Draft a weekly recap." />, {}, { path: "/gtm/strategy" });
    const link = await screen.findByRole("link", { name: /How to automate this/ });
    expect(link).toHaveAttribute("href", AUTOMATE_GUIDE_URL);
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("shows nothing without a description", async () => {
    renderGtm(<SuggestedWorkflow text="  " />, {}, { path: "/gtm/strategy" });
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("gtm-suggested-workflow")).not.toBeInTheDocument();
  });
});
