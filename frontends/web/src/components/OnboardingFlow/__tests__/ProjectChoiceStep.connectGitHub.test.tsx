/**
 * "Connect GitHub" on the project-choice step. A user who already has a
 * GitHub credential — for example one captured at sign-in — goes straight to
 * the repo picker; only a user without one is sent through the connect flow.
 */
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  connectGitHub: vi.fn(),
  getCredential: vi.fn(),
  credential: { hasToken: false, isLoading: false },
  updatePlan: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("@/lib/github-connect", () => ({ connectGitHub: mocks.connectGitHub }));
vi.mock("@/services/controlPlane/git", () => ({
  gitService: { getCredential: mocks.getCredential },
}));
vi.mock("@/hooks/useGitHubCredential", () => ({
  useGitHubCredential: () => mocks.credential,
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCompleteOnboarding: () => ({ mutateAsync: vi.fn() }),
}));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("@/lib/logger", () => ({ logger: { warn: vi.fn(), info: vi.fn(), error: vi.fn() } }));
vi.mock("../useOnboardingComplete", () => ({
  ensureProject: vi.fn(),
  finalizeOnboardingSideEffects: vi.fn(),
}));
vi.mock("../leaveOnboarding", () => ({ leaveOnboarding: vi.fn() }));
vi.mock("../analytics", () => ({ markOnboardingFinalized: vi.fn() }));
vi.mock("../ProvisioningGate", () => ({ ProvisioningGate: () => null }));
vi.mock("../useCommitLaunchPlan", () => ({
  useCommitLaunchPlan: () => ({ commit: null, runCommit: vi.fn(), retry: vi.fn() }),
}));

import { ProjectChoiceStep } from "../steps/ProjectChoiceStep";

const renderStep = () =>
  render(
    <ProjectChoiceStep
      plan={{ compute: "cloud_free_trial" }}
      updatePlan={mocks.updatePlan}
      onNext={vi.fn()}
      onBack={vi.fn()}
    />,
  );

beforeEach(() => {
  vi.clearAllMocks();
  mocks.credential.hasToken = false;
  mocks.credential.isLoading = false;
  mocks.updatePlan.mockResolvedValue(undefined);
  mocks.connectGitHub.mockResolvedValue({ kind: "redirecting" });
  mocks.getCredential.mockResolvedValue({ hasToken: false });
});

describe("ProjectChoiceStep — Connect GitHub", () => {
  it("skips the OAuth hop when a credential already exists", async () => {
    mocks.credential.hasToken = true;
    renderStep();

    await userEvent.click(screen.getByRole("button", { name: /connect github/i }));

    await waitFor(() => expect(mocks.updatePlan).toHaveBeenCalledWith({ intent: "existing_codebase" }));
    expect(mocks.connectGitHub).not.toHaveBeenCalled();
  });

  it("asks the server when the credential query has not answered yet", async () => {
    mocks.credential.isLoading = true;
    mocks.getCredential.mockResolvedValue({ hasToken: true });
    renderStep();

    await userEvent.click(screen.getByRole("button", { name: /connect github/i }));

    await waitFor(() => expect(mocks.getCredential).toHaveBeenCalledWith("github"));
    expect(mocks.connectGitHub).not.toHaveBeenCalled();
  });

  it("connects GitHub when there is no credential", async () => {
    renderStep();

    await userEvent.click(screen.getByRole("button", { name: /connect github/i }));

    await waitFor(() => expect(mocks.connectGitHub).toHaveBeenCalledTimes(1));
  });

  it("shows the failure and re-enables the button", async () => {
    mocks.connectGitHub.mockResolvedValue({ kind: "error", message: "Not signed in" });
    renderStep();

    await userEvent.click(screen.getByRole("button", { name: /connect github/i }));

    expect(await screen.findByText("Not signed in")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /connect github/i })).toBeEnabled();
  });

  it("re-enables the button after a desktop flow finishes in place", async () => {
    mocks.connectGitHub.mockResolvedValue({ kind: "connected", needsInstall: false });
    renderStep();

    await userEvent.click(screen.getByRole("button", { name: /connect github/i }));

    await waitFor(() => expect(screen.getByRole("button", { name: /connect github/i })).toBeEnabled());
  });
});
