// Copyright (c) 2025 Reliant Labs

/**
 * The queued-deploy banner says the control plane's words and offers the ONE
 * action the viewer can take: billing for someone who can set it up, the link
 * to send for someone who cannot. Offering checkout to a member would strand
 * them at a purchase they may not make.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { LiveHold } from "@/services/forge/live";

import { QueuedDeployBanner } from "../QueuedDeployBanner";

const NOW = Date.parse("2026-10-06T12:05:00.000Z");

function hold(overrides: Partial<LiveHold> = {}): LiveHold {
  return {
    kind: "billing",
    promotionId: "promo-2",
    reason: "this runs compute (1 workload) and the organization has no active compute plan",
    fix: "Subscribe to a Reliant Compute plan in Reliant → Settings → Billing (an org admin can). The deploy starts automatically once the plan is active; nothing needs to be re-run.",
    actionUrl: "https://app.reliant.dev/forge/env/prod?forgeProject=hounders",
    callerCanResolve: true,
    heldSince: "2026-10-06T12:00:00.000Z",
    ...overrides,
  };
}

describe("QueuedDeployBanner", () => {
  it("renders nothing when nothing is queued", () => {
    const { container } = render(<QueuedDeployBanner release="v13" holds={[]} onSetUpBilling={() => {}} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("states the release, the control plane's reason and remedy, and how long it has waited", () => {
    render(<QueuedDeployBanner release="v13" holds={[hold()]} onSetUpBilling={() => {}} now={NOW} />);
    expect(screen.getByRole("heading", { name: "Waiting on billing" })).toBeInTheDocument();
    expect(screen.getByTestId("queued-deploy-reason")).toHaveTextContent(
      "Release v13 is recorded. This runs compute (1 workload) and the organization has no active compute plan."
    );
    expect(screen.getByTestId("queued-deploy-fix")).toHaveTextContent(/nothing needs to be re-run/);
    expect(screen.getByText("Queued 5 minutes ago")).toBeInTheDocument();
  });

  it("sends someone who can set up billing to billing", async () => {
    const onSetUpBilling = vi.fn();
    render(<QueuedDeployBanner release="v13" holds={[hold()]} onSetUpBilling={onSetUpBilling} />);
    await userEvent.click(screen.getByTestId("queued-deploy-set-up-billing"));
    expect(onSetUpBilling).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId("queued-deploy-copy-link")).not.toBeInTheDocument();
  });

  it("tells a member who can, and hands them the control plane's link to send", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

    render(<QueuedDeployBanner release="v13" holds={[hold({ callerCanResolve: false })]} onSetUpBilling={() => {}} />);
    expect(screen.queryByTestId("queued-deploy-set-up-billing")).not.toBeInTheDocument();
    expect(screen.getByTestId("queued-deploy-ask")).toHaveTextContent(
      "Only an organization admin can set up billing. Send them this page:"
    );
    await userEvent.click(screen.getByTestId("queued-deploy-copy-link"));
    expect(writeText).toHaveBeenCalledWith("https://app.reliant.dev/forge/env/prod?forgeProject=hounders");
  });

  /**
   * With no app URL configured the control plane sends no link, and this
   * window's own address is no substitute: in the desktop app it is a local
   * URL nobody else can open.
   */
  it("offers no link to copy when the control plane has none", () => {
    render(
      <QueuedDeployBanner
        release="v13"
        holds={[hold({ callerCanResolve: false, actionUrl: "" })]}
        onSetUpBilling={() => {}}
      />
    );
    expect(screen.queryByTestId("queued-deploy-copy-link")).not.toBeInTheDocument();
    expect(screen.getByTestId("queued-deploy-ask")).toHaveTextContent(/goes out on its own once they have/);
  });
});
