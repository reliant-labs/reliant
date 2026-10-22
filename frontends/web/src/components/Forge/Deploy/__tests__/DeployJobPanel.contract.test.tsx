// Copyright (c) 2025 Reliant Labs

/**
 * THE JOB PANEL CONTRACT: three things that are not success and not failure.
 *
 * A running job, an indeterminate job, and a completed job whose rollout did not
 * establish anything. None of them may render as either outcome, and the panel
 * must keep the JOB's lifecycle separate from the DEPLOY's verdict — `completed`
 * says forge finished, not that the environment converged.
 *
 * Pure props throughout: reaching any of these against a live daemon means
 * deploying to a real cluster.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { DeployJobPanel } from "../DeployJobPanel";
import { appliedReport, meta, planOutcome } from "./fixtures";

function baseProps() {
  return {
    handle: "dep-abc123",
    env: "prod",
    startedAt: "2026-09-10T14:00:00Z",
    onClose: () => {},
  };
}

describe("a running job", () => {
  it("renders as in-progress, not success", () => {
    render(<DeployJobPanel {...baseProps()} jobStatus="running" report={null} />);

    expect(screen.getByTestId("deploy-job").getAttribute("data-job-status")).toBe("running");
    expect(screen.getByTestId("deploy-job-heading").textContent).toMatch(/in flight/i);
    // Explicitly neither outcome.
    expect(screen.getByTestId("deploy-job-blurb").textContent).toMatch(
      /neither a success nor a failure/i
    );

    // The unfilled, dashed treatment: nothing has been established.
    const status = screen.getByTestId("deploy-job-status");
    expect(status.className).toContain("border-dashed");
    expect(status.className).toContain("bg-transparent");

    // No verdict is invented while the outcome does not exist.
    expect(screen.queryByTestId("deploy-verdict")).toBeNull();
    expect(screen.getByTestId("deploy-job-running-note")).toBeTruthy();

    // And closing does not claim to stop it.
    expect(screen.getByText(/keeps running/i)).toBeTruthy();
  });
});

describe("an indeterminate job", () => {
  it("renders as unknown with the may-or-may-not-have-landed caveat", () => {
    // A killed job, a dead process, an unparseable report, or a handle a restarted
    // daemon no longer recognises — all arrive here, and all mean the same thing.
    render(
      <DeployJobPanel
        {...baseProps()}
        jobStatus="unknown"
        jobStatusDetail="the daemon restarted and lost this handle"
        report={null}
      />
    );

    expect(screen.getByTestId("deploy-job-heading").textContent).toMatch(
      /may or may not have reached the cluster/i
    );

    const unknown = screen.getByTestId("deploy-job-unknown");
    expect(unknown.textContent).toMatch(/may or may not have reached the cluster/i);
    // Not a failure, and explicitly not a retry.
    expect(unknown.textContent).toMatch(/do not start another deploy/i);
    expect(unknown.textContent).not.toMatch(/deploy failed/i);

    // The server's own sentence is shown, not parsed.
    expect(screen.getByTestId("deploy-job-detail").textContent).toContain("lost this handle");
  });

  it("offers verifying the environment as the resolving action", async () => {
    const user = userEvent.setup();
    const onVerify = vi.fn();
    render(
      <DeployJobPanel {...baseProps()} jobStatus="unknown" report={null} onVerify={onVerify} />
    );
    await user.click(screen.getByTestId("deploy-job-verify"));
    expect(onVerify).toHaveBeenCalledTimes(1);
  });

  it("keeps `failed` as the one disposition where nothing can have shipped", () => {
    render(<DeployJobPanel {...baseProps()} jobStatus="failed" report={null} />);
    const heading = screen.getByTestId("deploy-job-heading").textContent ?? "";
    expect(heading).toMatch(/never started/i);
    expect(heading).toMatch(/nothing was applied/i);
    // No unknown-outcome panel: this is a determinate answer.
    expect(screen.queryByTestId("deploy-job-unknown")).toBeNull();
  });
});

describe("a completed job's verdict", () => {
  it("does not treat `completed` as a successful deploy", () => {
    // Forge finished and reported. Whether it worked is the report's question, so
    // the job badge carries no success hue.
    render(
      <DeployJobPanel
        {...baseProps()}
        jobStatus="completed"
        report={planOutcome(
          appliedReport({
            mode: "wait",
            timeout_seconds: 300,
            results: [{ kind: "Deployment", name: "admin-server", state: "timed_out" }],
            ready: 0,
            failed: 0,
            timed_out: 1,
            not_waited: 0,
          })
        )}
      />
    );

    const jobStatus = screen.getByTestId("deploy-job-status");
    expect(jobStatus.className).toContain("border-dashed");
    expect(jobStatus.className).not.toContain("border-success");

    // The verdict is where the truth is, and it is UNKNOWN.
    const verdict = screen.getByTestId("deploy-verdict");
    expect(verdict.getAttribute("data-certainty")).toBe("unknown");
    expect(screen.getByTestId("deploy-verdict-label").textContent).toBe("Not known");
    expect(screen.getByTestId("deploy-verdict-blurb").textContent).toMatch(
      /did not report ready inside the budget/i
    );
    expect(screen.getByTestId("deploy-verdict-blurb").textContent).toMatch(/verify the environment/i);
  });

  it("renders a fully-ready wait as known-good", () => {
    render(
      <DeployJobPanel
        {...baseProps()}
        jobStatus="completed"
        report={planOutcome(
          appliedReport({
            mode: "wait",
            timeout_seconds: 300,
            results: [
              { kind: "Deployment", name: "admin-server", state: "ready" },
              { kind: "Deployment", name: "litellm", state: "ready" },
            ],
            ready: 2,
            failed: 0,
            timed_out: 0,
            not_waited: 0,
          })
        )}
      />
    );
    expect(screen.getByTestId("deploy-verdict").getAttribute("data-certainty")).toBe("known-good");
    expect(screen.getByTestId("deploy-verdict-blurb").textContent).toMatch(/became ready/i);
  });

  it("renders a failure as known-bad", () => {
    render(
      <DeployJobPanel
        {...baseProps()}
        jobStatus="completed"
        report={planOutcome(
          appliedReport(
            {
              mode: "wait",
              timeout_seconds: 300,
              results: [{ kind: "Deployment", name: "admin-server", state: "failed" }],
              ready: 0,
              failed: 1,
              timed_out: 0,
              not_waited: 0,
            },
            { ok: false, exit_code: 1, error: "admin-server never became ready" }
          )
        )}
      />
    );
    expect(screen.getByTestId("deploy-verdict").getAttribute("data-certainty")).toBe("known-bad");
  });

  it("renders a skip rollout as UNKNOWN, saying nothing was observed converging", () => {
    render(
      <DeployJobPanel
        {...baseProps()}
        jobStatus="completed"
        report={planOutcome(
          appliedReport({
            mode: "skip",
            timeout_seconds: 0,
            results: [{ kind: "Deployment", name: "admin-server", state: "not_waited" }],
            ready: 0,
            failed: 0,
            timed_out: 0,
            not_waited: 1,
          })
        )}
      />
    );
    // forge exited 0 and applied cleanly — and still nothing is known.
    expect(screen.getByTestId("deploy-verdict").getAttribute("data-certainty")).toBe("unknown");
    expect(screen.getByTestId("deploy-verdict-blurb").textContent).toMatch(
      /nothing was observed converging/i
    );
    expect(screen.getByTestId("deploy-verdict-blurb").textContent).toMatch(
      /neither healthy nor broken/i
    );
  });

  it("does not read an unreadable report as either outcome", () => {
    render(
      <DeployJobPanel
        {...baseProps()}
        jobStatus="completed"
        report={{ kind: "malformed", meta: meta(), raw: "{{" }}
      />
    );
    // No verdict is claimed from a document that could not be read.
    expect(screen.queryByTestId("deploy-verdict")).toBeNull();
  });
});
