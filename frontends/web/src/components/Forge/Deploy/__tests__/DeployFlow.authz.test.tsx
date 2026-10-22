// Copyright (c) 2025 Reliant Labs

/**
 * TWO BLOCKED STATES THAT ARE NOT THE SAME PROBLEM, and used to render as one.
 *
 * A hosted deploy can fail before it plans for two completely different
 * reasons, and the fix for each is something the other person cannot do:
 *
 *   forge isn't authorized      The machine has no control-plane credential.
 *                               The user CAN fix this themselves — sign in to
 *                               Reliant, which deposits the credential into
 *                               forge's store (internal/cliauth.DepositForForge).
 *   permission not held         The user is signed in, and their ORG
 *                               PERMISSIONS do not include what this deploy
 *                               needs. No amount of signing in changes it; an
 *                               org admin has to grant the permission.
 *
 * Both used to surface as "Could not reach your daemon to plan this deploy",
 * which is not merely vague — it is FALSE. The daemon was reached and answered.
 * Sending a user to debug daemon connectivity over a permissions problem is the
 * specific failure these pin.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { DeployFlow } from "../DeployFlow";

function noop() {}

function renderWithPlanError(message: string) {
  render(
    <DeployFlow
      planOutcome={undefined}
      isPlanning={false}
      planError={new Error(message)}
      isStarting={false}
      onConfirm={noop}
      onReplan={noop}
      onClose={noop}
    />
  );
}

describe("DeployFlow — authorization is distinct from unreachability", () => {
  it("names signing in as the fix when forge has no credential", () => {
    // forge's own wording, from internal/cloud.ErrNoCredential.
    renderWithPlanError(
      "no control-plane credential for https://admin.reliantapi.com: run `forge login`"
    );

    const panel = screen.getByTestId("deploy-not-authorized");
    expect(panel).toBeInTheDocument();
    // The fix has to be the one that works HERE: signing in to Reliant
    // deposits the credential into forge's store. Telling a user to run
    // `forge login` in a daemon they cannot open a browser in is a dead end.
    expect(panel.textContent).toMatch(/sign in/i);
    // And it must NOT claim the daemon was unreachable.
    expect(screen.queryByTestId("deploy-plan-error")).not.toBeInTheDocument();
  });

  it("names the missing permission, and who can grant it", () => {
    renderWithPlanError(
      "permission_denied: your organization permissions do not include deploy:write"
    );

    const panel = screen.getByTestId("deploy-permission-denied");
    expect(panel).toBeInTheDocument();
    // The permission itself, so the user can ask for the right thing.
    expect(panel.textContent).toContain("deploy:write");
    // And who to ask. A refusal with no route forward is a dead end.
    expect(panel.textContent).toMatch(/admin/i);
    expect(screen.queryByTestId("deploy-plan-error")).not.toBeInTheDocument();
  });

  it("does not offer a re-plan for a permissions problem", () => {
    // Re-planning cannot change authority, so offering it invites a loop that
    // always ends in the same refusal.
    renderWithPlanError(
      "permission_denied: your organization permissions do not include secret:write"
    );
    expect(screen.queryByTestId("deploy-blocked-replan")).not.toBeInTheDocument();
  });

  it("still reports a genuine transport failure as one", () => {
    // The discrimination must not swallow real unreachability — that would
    // trade one misdiagnosis for another.
    renderWithPlanError("connection refused");
    expect(screen.getByTestId("deploy-plan-error")).toBeInTheDocument();
    expect(screen.queryByTestId("deploy-not-authorized")).not.toBeInTheDocument();
    expect(screen.queryByTestId("deploy-permission-denied")).not.toBeInTheDocument();
  });

  it("treats an expired host credential as not-authorized, not as a permissions problem", () => {
    // An expired deposit is fixed by signing in again, exactly like a missing
    // one — forge says so itself, and it is not an org-permission question.
    renderWithPlanError(
      "no control-plane credential for https://admin.reliantapi.com: the stored credential expired at 2026-01-01T00:00:00Z"
    );
    expect(screen.getByTestId("deploy-not-authorized")).toBeInTheDocument();
    expect(screen.queryByTestId("deploy-permission-denied")).not.toBeInTheDocument();
  });
});
