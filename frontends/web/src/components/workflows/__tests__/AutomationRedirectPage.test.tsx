// Copyright (c) 2025 Reliant Labs

/**
 * /workflows/automations/$triggerId is a redirect: the trigger is looked up
 * and the page replaces itself with the trigger's row on its workflow's page.
 * A trigger that is gone says so and offers what is on.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { ConnectError, Code } from "@connectrpc/connect";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { protoTrigger, renderWorkflowsPage } from "./workflowsTestUtils";
import { AutomationRedirectPage } from "../AutomationRedirectPage";

const mocks = vi.hoisted(() => ({ getTrigger: vi.fn() }));

beforeEach(() => {
  mocks.getTrigger.mockReset();
  setTransport(mockTransport({ ...serviceHandlers("TriggerService", { getTrigger: mocks.getTrigger }) }));
});

function renderAt(id: string) {
  return renderWorkflowsPage(<AutomationRedirectPage />, `/workflows/automations/${id}`, "/workflows/automations/$triggerId");
}

describe("AutomationRedirectPage", () => {
  it("replaces itself with the trigger's row on its workflow's page", async () => {
    mocks.getTrigger.mockResolvedValue({ trigger: protoTrigger("t1", "triage") });
    const { router } = renderAt("t1");
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/library/triage"));
    expect(router.state.location.search).toMatchObject({ project: "proj-1" });
    expect(router.state.location.hash).toBe("trigger-t1");
    // Replaced, so Back skips the dead URL.
    expect(router.history.length).toBe(1);
  });

  it("says a trigger that is gone no longer exists", async () => {
    mocks.getTrigger.mockRejectedValue(new ConnectError("trigger not found", Code.NotFound));
    renderAt("gone");
    expect(await screen.findByTestId("automation-gone")).toHaveTextContent("This automation no longer exists.");
    expect(screen.getByRole("link", { name: "Show what is on" })).toHaveAttribute("href", expect.stringContaining("/workflows/library?"));
  });
});
