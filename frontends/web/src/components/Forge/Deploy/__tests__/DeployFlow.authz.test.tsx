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
import { Code, ConnectError } from "@connectrpc/connect";

import { DeployFlow } from "../DeployFlow";

function noop() {}

/**
 * The plan rpc's error as reliant's ForgeService sends it: forge's own words,
 * with forge's reason (and, for scope_missing, its scopes) carried onto the
 * rpc error. The reason is the signal; the message is display copy.
 */
function refusal(message: string, code: Code, reason?: string, scopes?: string): ConnectError {
  const metadata = new Headers();
  if (reason) metadata.set("x-forge-error-reason", reason);
  if (scopes) metadata.set("x-forge-error-scopes", scopes);
  return new ConnectError(message, code, metadata);
}

function renderWithPlanError(planError: Error) {
  render(
    <DeployFlow
      planOutcome={undefined}
      isPlanning={false}
      planError={planError}
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
      refusal(
        "no control-plane credential for https://admin.reliantapi.com: run `forge login`",
        Code.FailedPrecondition,
        "no_control_plane_credential",
      )
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
      refusal(
        "your organization permissions do not include deploy:write",
        Code.PermissionDenied,
        "scope_missing",
        "deploy:write",
      )
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
      refusal(
        "your organization permissions do not include secret:write",
        Code.PermissionDenied,
        "scope_missing",
        "secret:write",
      )
    );
    expect(screen.queryByTestId("deploy-blocked-replan")).not.toBeInTheDocument();
  });

  it("still reports a genuine transport failure as one", () => {
    // The discrimination must not swallow real unreachability — that would
    // trade one misdiagnosis for another.
    renderWithPlanError(new Error("connection refused"));
    expect(screen.getByTestId("deploy-plan-error")).toBeInTheDocument();
    expect(screen.queryByTestId("deploy-not-authorized")).not.toBeInTheDocument();
    expect(screen.queryByTestId("deploy-permission-denied")).not.toBeInTheDocument();
  });

  it("treats an expired host credential as not-authorized, not as a permissions problem", () => {
    // An expired deposit is fixed by signing in again, exactly like a missing
    // one — forge says so itself, and it is not an org-permission question.
    renderWithPlanError(
      refusal(
        "no control-plane credential for https://admin.reliantapi.com: the stored credential expired at 2026-01-01T00:00:00Z",
        Code.FailedPrecondition,
        "no_control_plane_credential",
      )
    );
    expect(screen.getByTestId("deploy-not-authorized")).toBeInTheDocument();
    expect(screen.queryByTestId("deploy-permission-denied")).not.toBeInTheDocument();
  });

  it("reads the reason, not the words: a reasonless refusal stays a transport error", () => {
    // A daemon whose forge predates error reasons degrades to the plain
    // error (13-p9 D3) rather than to a guessed diagnosis.
    renderWithPlanError(
      refusal("your organization permissions do not include deploy:write", Code.PermissionDenied)
    );
    expect(screen.getByTestId("deploy-plan-error")).toBeInTheDocument();
  });
});
