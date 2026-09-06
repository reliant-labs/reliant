/**
 * How a partial failure reaches the user.
 *
 * The property under test is the one the design is most emphatic about: a user
 * who paid and got SOMETHING must be able to continue. Blocking them in the
 * wizard to punish a provisioning failure is the worst available response — so
 * `Continue` is enabled on `partial`, not only on `complete`, and the failing
 * task's own reason is on screen next to it.
 */
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ProvisioningGate } from "../ProvisioningGate";
import type { CommitResult } from "../commitLaunchPlan";

// The daemon gate polls the control plane and owns the "your machine is still
// starting" narration. Stubbed here so this file tests the checklist and the
// continue/retry affordances rather than re-testing a gate that has its own
// suite.
vi.mock("../DaemonConnectingGate", () => ({
  DaemonConnectingGate: ({ daemonRef }: { daemonRef?: string }) => (
    <div data-testid="daemon-gate" data-daemon-ref={daemonRef} />
  ),
}));

function makeCommit(overrides: Partial<CommitResult> = {}): CommitResult {
  return {
    commitKey: "key-1",
    status: "complete",
    tasks: [
      { name: "grant_ai_access", status: "complete", detail: "AI access ready." },
      {
        name: "provision_daemon",
        status: "complete",
        detail: "Starting your machine…",
        daemonId: "d-1",
      },
    ],
    daemonId: "d-1",
    ...overrides,
  };
}

describe("ProvisioningGate", () => {
  it("lets a partially-failed commit continue, and names what went wrong", () => {
    const onContinue = vi.fn();
    render(
      <ProvisioningGate
        commit={makeCommit({
          status: "partial",
          daemonId: undefined,
          tasks: [
            {
              name: "grant_ai_access",
              status: "complete",
              detail: "AI access ready.",
            },
            {
              name: "provision_daemon",
              status: "failed",
              detail: "daemon size not allowed on your plan",
            },
          ],
        })}
        onContinue={onContinue}
      />,
    );

    // The SERVER's reason, verbatim. "Size not allowed" and "minutes
    // exhausted" need different actions, and a generic error erases that.
    expect(
      screen.getByText("daemon size not allowed on your plan"),
    ).toBeInTheDocument();
    expect(screen.getByText("AI access ready.")).toBeInTheDocument();

    const cont = screen.getByRole("button", { name: /Continue/i });
    expect(cont).toBeEnabled();
    fireEvent.click(cont);
    expect(onContinue).toHaveBeenCalled();
  });

  // A commit that retried itself would be a billable call firing without a
  // user asking for it — the same defect as provisioning from an effect, on a
  // timer. So retry exists, and it is a button.
  it("offers retry only on a failed or partial commit, and only via a click", () => {
    const onRetry = vi.fn();
    const { rerender } = render(
      <ProvisioningGate
        commit={makeCommit({ status: "partial", daemonId: undefined })}
        onContinue={vi.fn()}
        onRetry={onRetry}
      />,
    );

    expect(onRetry).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /Try again/i }));
    expect(onRetry).toHaveBeenCalledTimes(1);

    rerender(
      <ProvisioningGate
        commit={makeCommit({ status: "complete", daemonId: undefined })}
        onContinue={vi.fn()}
        onRetry={onRetry}
      />,
    );
    expect(
      screen.queryByRole("button", { name: /Try again/i }),
    ).not.toBeInTheDocument();
  });

  it("quotes the commit key when everything failed, so a charge can reach a human", () => {
    render(
      <ProvisioningGate
        commit={makeCommit({
          status: "failed",
          daemonId: undefined,
          tasks: [
            { name: "grant_ai_access", status: "failed", detail: "no key" },
            { name: "provision_daemon", status: "failed", detail: "no machine" },
          ],
        })}
        onContinue={vi.fn()}
      />,
    );

    expect(screen.getByText("key-1")).toBeInTheDocument();
  });

  // Reuse, not replacement: when there is a machine to wait for, the existing
  // gate does the waiting. Its timeout and failure phases are the product of a
  // real bug fix, and a second waiting surface would drift from them.
  it("hands the daemon wait to DaemonConnectingGate, with the right daemon id", () => {
    render(<ProvisioningGate commit={makeCommit()} onContinue={vi.fn()} />);

    const gate = screen.getByTestId("daemon-gate");
    expect(gate).toBeInTheDocument();
    expect(gate).toHaveAttribute("data-daemon-ref", "d-1");
  });

  // The free path — local compute, own API key — commits nothing. Making that
  // user watch a checklist of work that did not happen would be theatre.
  it("does not make a no-op commit watch a checklist", () => {
    const onContinue = vi.fn();
    render(
      <ProvisioningGate
        commit={makeCommit({
          daemonId: undefined,
          tasks: [
            { name: "grant_ai_access", status: "skipped", detail: "" },
            { name: "provision_daemon", status: "skipped", detail: "" },
          ],
        })}
        onContinue={onContinue}
      />,
    );

    expect(screen.queryByTestId("provisioning-gate")).not.toBeInTheDocument();
    expect(
      screen.getByTestId("provisioning-gate-nothing-to-do"),
    ).toBeInTheDocument();
  });
});
