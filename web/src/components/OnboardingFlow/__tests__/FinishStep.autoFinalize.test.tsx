/**
 * FinishStep — local compute finalizes onto a default project with no picker.
 *
 * What must hold: it runs the sequence exactly once (StrictMode double-invokes
 * effects), shows a "Setting up your project" state meanwhile, never retries
 * billable work on its own, and hands the commit to ProvisioningGate.
 */
import { StrictMode } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LaunchPlan } from "../types";

const mocks = vi.hoisted(() => ({
  ensureProject: vi.fn(async (_plan: unknown) => "proj-1"),
  finalizeSideEffects: vi.fn(async () => undefined),
  complete: vi.fn(async (_vars: unknown) => ({})),
  runCommit: vi.fn(async (_plan: unknown) => ({ ok: true })),
  retry: vi.fn(async (_plan: unknown) => ({ ok: true })),
  commit: null as null | { ok: boolean },
  markFinalized: vi.fn(),
  leave: vi.fn(async () => undefined),
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCompleteOnboarding: () => ({ mutateAsync: mocks.complete }),
}));
vi.mock("../useOnboardingComplete", () => ({
  ensureProject: mocks.ensureProject,
  finalizeOnboardingSideEffects: mocks.finalizeSideEffects,
}));
vi.mock("../useCommitLaunchPlan", () => ({
  useCommitLaunchPlan: () => ({
    commit: mocks.commit,
    running: false,
    runCommit: mocks.runCommit,
    retry: mocks.retry,
  }),
}));
vi.mock("../leaveOnboarding", () => ({ leaveOnboarding: mocks.leave }));
vi.mock("../analytics", () => ({ markOnboardingFinalized: mocks.markFinalized }));
vi.mock("../ProvisioningGate", () => ({
  ProvisioningGate: () => <div data-testid="provisioning-gate" />,
}));
vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));

import { FinishStep } from "../steps/FinishStep";

const PLAN: Partial<LaunchPlan> = {
  compute: "local_daemon",
  modelProvider: "anthropic",
};

function renderStep() {
  return render(
    <StrictMode>
      <FinishStep plan={PLAN} updatePlan={vi.fn()} onNext={vi.fn()} onBack={vi.fn()} />
    </StrictMode>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.commit = null;
  mocks.ensureProject.mockImplementation(async () => "proj-1");
});

describe("FinishStep", () => {
  it("shows a setup state and runs the finalize sequence exactly once", async () => {
    renderStep();
    expect(screen.getByText(/Setting up your project/i)).toBeInTheDocument();
    await waitFor(() => expect(mocks.runCommit).toHaveBeenCalledTimes(1));
    expect(mocks.ensureProject).toHaveBeenCalledTimes(1);
    expect(mocks.complete).toHaveBeenCalledTimes(1);
    expect(mocks.finalizeSideEffects).toHaveBeenCalledTimes(1);
    expect(mocks.markFinalized).toHaveBeenCalledTimes(1);
  });

  it("surfaces a failure with Retry and does not retry on its own", async () => {
    mocks.ensureProject.mockRejectedValueOnce(new Error("disk full"));
    renderStep();
    expect(await screen.findByText(/disk full/)).toBeInTheDocument();
    expect(mocks.ensureProject).toHaveBeenCalledTimes(1);
    expect(mocks.runCommit).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: /retry/i }));
    await waitFor(() => expect(mocks.runCommit).toHaveBeenCalledTimes(1));
    expect(mocks.ensureProject).toHaveBeenCalledTimes(2);
  });

  it("renders the provisioning gate once a commit exists", () => {
    mocks.commit = { ok: true };
    renderStep();
    expect(screen.getByTestId("provisioning-gate")).toBeInTheDocument();
  });
});
