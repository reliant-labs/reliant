// Copyright (c) 2025 Reliant Labs

/**
 * A PROTECTED environment's secrets (design D6), end to end through the real
 * section: the badge, the refused write, the code dialog, and the retry.
 *
 * Only the RPCs are fakes. isStepUpRequired, the gate, the reducer and both
 * dialog hosts are the real ones, so a refusal here is recognised the way a
 * real control plane's would be: FailedPrecondition carrying the
 * `step_up_required` reason in `x-forge-error-reason`.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import type { LiveEnv } from "@/services/forge/live";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));

const listSecrets = vi.fn();
const setSecret = vi.fn();
const deleteSecret = vi.fn();
const getSecretVersions = vi.fn();
const beginStepUp = vi.fn();
const completeStepUp = vi.fn();

vi.mock("@/services/forge/secretStore", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/secretStore")>()),
  listSecrets: (...args: unknown[]) => listSecrets(...args),
  setSecret: (...args: unknown[]) => setSecret(...args),
  deleteSecret: (...args: unknown[]) => deleteSecret(...args),
  getSecretVersions: (...args: unknown[]) => getSecretVersions(...args),
  beginStepUp: (...args: unknown[]) => beginStepUp(...args),
  completeStepUp: (...args: unknown[]) => completeStepUp(...args),
}));

import { LiveSecretsSection } from "../LiveSecretsSection";

const SELF_MANAGED: LiveEnv = {
  id: "cp-prod",
  name: "prod",
  project: "hounders",
  kind: "self_managed",
  declaredBy: null,
  release: "v12",
  promotedByActor: "ci",
  promotedByUserId: "",
  phase: "succeeded",
  provenance: "",
  holds: [],
  declaredShape: {
    kind: "self_managed",
    workloads: [{ name: "api", runtime: "cluster", cluster: "gke" }],
    secrets: [{ name: "STRIPE_KEY", provider: "hosted", declaredBy: ["api"] }],
    domains: [],
    clusters: [],
  },
} as LiveEnv;

const SUMMARY = {
  name: "STRIPE_KEY",
  currentVersion: 3,
  oldestVersion: 1,
  maxVersions: 10,
  currentVersionDeleted: false,
  currentVersionDestroyed: false,
};

function stepUpRequired(): ConnectError {
  return new ConnectError(
    "this environment is protected: confirm with a step-up code first",
    Code.FailedPrecondition,
    new Headers({ "x-forge-error-reason": "step_up_required" })
  );
}

function renderSection(selectedSecret: string | null = null) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <LiveSecretsSection
        projectId="proj-1"
        env={SELF_MANAGED}
        forgeProject="hounders"
        selectedSecret={selectedSecret}
        onSelectSecret={vi.fn()}
      />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  listSecrets.mockResolvedValue({ secrets: [SUMMARY], protected: true });
  getSecretVersions.mockResolvedValue({
    summary: SUMMARY,
    versions: [
      { version: 3, createdAt: "2026-10-20T10:00:00Z", destroyed: false, setBy: { kind: "user", email: "alice@acme.dev" } },
      { version: 2, createdAt: "2026-10-19T10:00:00Z", destroyed: false, setBy: { kind: "platform" } },
      { version: 1, createdAt: "2026-10-18T10:00:00Z", destroyed: false, setBy: null },
    ],
  });
  beginStepUp.mockResolvedValue({
    sentTo: "a***@acme.dev",
    expiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
  });
  completeStepUp.mockResolvedValue({ validUntil: new Date(Date.now() + 15 * 60_000).toISOString() });
});

async function openAddForm(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByText("STRIPE_KEY");
  await user.click(screen.getByTestId("add-secret"));
  await user.type(await screen.findByLabelText("Name"), "NEW_KEY");
  await user.type(screen.getByLabelText("Value"), "s3cret-value");
}

describe("the protected badge", () => {
  it("shows the badge, the lock sentence and Confirm now when no grant is held", async () => {
    renderSection();
    expect(await screen.findByTestId("secrets-protected-badge")).toHaveTextContent("Protected");
    expect(screen.getByTestId("secrets-step-up-locked")).toBeInTheDocument();
    expect(screen.getByTestId("secrets-unlock")).toBeInTheDocument();
    // A self-managed env is delivered by External Secrets, and says so.
    expect(screen.getByText(/External Secrets/)).toBeInTheDocument();
  });

  it("counts down while the listing reports a live grant", async () => {
    listSecrets.mockResolvedValue({
      secrets: [SUMMARY],
      protected: true,
      stepUpValidUntil: new Date(Date.now() + 14 * 60_000 + 30_000).toISOString(),
    });
    renderSection();
    expect(await screen.findByTestId("secrets-step-up-countdown")).toHaveTextContent(/^14:(3\d|29)$/);
    expect(screen.queryByTestId("secrets-unlock")).toBeNull();
  });

  it("an unprotected environment shows neither", async () => {
    listSecrets.mockResolvedValue({ secrets: [SUMMARY], protected: false });
    renderSection();
    await screen.findByText("STRIPE_KEY");
    expect(screen.queryByTestId("secrets-protected-badge")).toBeNull();
    expect(screen.queryByTestId("secrets-step-up-locked")).toBeNull();
  });
});

describe("a refused save: request code → enter code → retry", () => {
  it("retries the SAME write once the code is confirmed, then closes", async () => {
    const user = userEvent.setup();
    setSecret.mockRejectedValueOnce(stepUpRequired()).mockResolvedValueOnce({ version: 1 });
    renderSection();
    await openAddForm(user);
    await user.click(screen.getByTestId("set-secret-submit"));

    // The panel replaces the form inside the same modal; nothing was sent yet.
    const panel = await screen.findByTestId("step-up");
    expect(panel).toHaveAttribute("data-phase", "intro");
    expect(screen.getByTestId("set-secret-form")).toHaveClass("hidden");
    expect(beginStepUp).not.toHaveBeenCalled();
    expect(screen.getAllByRole("dialog")).toHaveLength(1);

    await user.click(screen.getByTestId("step-up-send"));
    expect(await screen.findByTestId("step-up-sent")).toHaveTextContent("a***@acme.dev");
    expect(beginStepUp).toHaveBeenCalledWith("cp-prod");
    expect(screen.getByTestId("step-up-expires-in")).toHaveTextContent(/^(9:5\d|10:00)$/);

    // Confirm is disabled until the code is complete.
    expect(screen.getByTestId("step-up-confirm")).toBeDisabled();
    await user.type(screen.getByTestId("step-up-code"), "abcde-fghjk");
    await user.click(screen.getByTestId("step-up-confirm"));

    await waitFor(() => expect(setSecret).toHaveBeenCalledTimes(2));
    expect(completeStepUp).toHaveBeenCalledWith("cp-prod", "abcde-fghjk");
    // The retry is the user's write, unchanged — create-only (cas 0), same value.
    expect(setSecret.mock.calls[1]?.[0]).toEqual(setSecret.mock.calls[0]?.[0]);
    expect(setSecret.mock.calls[1]?.[0]).toMatchObject({
      environmentId: "cp-prod",
      name: "NEW_KEY",
      value: "s3cret-value",
      cas: 0,
    });
    await waitFor(() => expect(screen.queryByTestId("set-secret-form")).toBeNull());
  });

  it("a wrong code says how many attempts are left; the fifth exhausts the code", async () => {
    const user = userEvent.setup();
    setSecret.mockRejectedValue(stepUpRequired());
    completeStepUp.mockRejectedValue(new ConnectError("that step-up code is not valid", Code.PermissionDenied));
    renderSection();
    await openAddForm(user);
    await user.click(screen.getByTestId("set-secret-submit"));
    await user.click(await screen.findByTestId("step-up-send"));
    await screen.findByTestId("step-up-sent");

    const code = screen.getByTestId("step-up-code");
    await user.type(code, "WRONGWRONG");
    await user.click(screen.getByTestId("step-up-confirm"));
    expect(await screen.findByTestId("step-up-invalid")).toHaveTextContent("4 attempts left");

    for (let i = 0; i < 4; i++) {
      await waitFor(() => expect(screen.getByTestId("step-up-confirm")).toBeEnabled());
      await user.click(screen.getByTestId("step-up-confirm"));
    }
    expect(await screen.findByTestId("step-up-exhausted")).toBeInTheDocument();
    expect(completeStepUp).toHaveBeenCalledTimes(5);
    expect(screen.queryByTestId("step-up-code")).toBeNull();
    // A new code is a minute away: the button counts down rather than failing.
    expect(screen.getByTestId("step-up-send")).toBeDisabled();
    expect(screen.getByTestId("step-up-send")).toHaveTextContent(/Send a new code in (0:\d\d|1:00)/);
    // Nothing was saved.
    expect(setSecret).toHaveBeenCalledTimes(1);
  });

  it("rate-limited: a code from a minute ago is in the inbox, so the dialog asks for it", async () => {
    const user = userEvent.setup();
    setSecret.mockRejectedValueOnce(stepUpRequired()).mockResolvedValueOnce({ version: 1 });
    beginStepUp.mockRejectedValue(
      new ConnectError("a code was sent a moment ago; check your email or wait a minute", Code.ResourceExhausted)
    );
    renderSection();
    await openAddForm(user);
    await user.click(screen.getByTestId("set-secret-submit"));
    await user.click(await screen.findByTestId("step-up-send"));

    expect(await screen.findByTestId("step-up-rate-limited")).toBeInTheDocument();
    expect(screen.getByTestId("step-up-sent")).toHaveTextContent(/a moment ago/);
    expect(screen.getByTestId("step-up-resend")).toBeDisabled();

    await user.type(screen.getByTestId("step-up-code"), "ABCDEFGHJK");
    await user.click(screen.getByTestId("step-up-confirm"));
    await waitFor(() => expect(setSecret).toHaveBeenCalledTimes(2));
  });

  it("cancelling saves nothing and says why", async () => {
    const user = userEvent.setup();
    setSecret.mockRejectedValue(stepUpRequired());
    renderSection();
    await openAddForm(user);
    await user.click(screen.getByTestId("set-secret-submit"));
    await user.click(await screen.findByTestId("step-up-cancel"));

    expect(await screen.findByTestId("set-secret-step-up-needed")).toHaveTextContent(/Nothing was saved/);
    expect(screen.getByTestId("set-secret-form")).not.toHaveClass("hidden");
    // Not mistaken for a concurrent edit.
    expect(screen.queryByText(/Someone else wrote/)).toBeNull();
    expect(setSecret).toHaveBeenCalledTimes(1);
  });

  it("a non-admin is told who can make the change, and no code is sent", async () => {
    const user = userEvent.setup();
    setSecret.mockRejectedValue(stepUpRequired());
    beginStepUp.mockRejectedValue(new ConnectError("forbidden", Code.PermissionDenied));
    renderSection();
    await openAddForm(user);
    await user.click(screen.getByTestId("set-secret-submit"));
    await user.click(await screen.findByTestId("step-up-send"));
    expect(await screen.findByTestId("step-up-failed")).toHaveTextContent(/Only an organization admin/);
  });

  it("a deployment that cannot send codes says the environment is read-only here", async () => {
    const user = userEvent.setup();
    setSecret.mockRejectedValue(stepUpRequired());
    beginStepUp.mockRejectedValue(new ConnectError("step-up is not available", Code.Unavailable));
    renderSection();
    await openAddForm(user);
    await user.click(screen.getByTestId("set-secret-submit"));
    await user.click(await screen.findByTestId("step-up-send"));
    expect(await screen.findByTestId("step-up-failed")).toHaveTextContent(/read-only here/);
  });
});

describe("row actions and Confirm now use their own dialog", () => {
  it("a refused delete confirms in a dialog of its own and is retried", async () => {
    const user = userEvent.setup();
    deleteSecret.mockRejectedValueOnce(stepUpRequired()).mockResolvedValueOnce(undefined);
    renderSection("STRIPE_KEY");

    // Who set each version, from the audit trail.
    expect(await screen.findByTestId("version-author-3")).toHaveTextContent("by alice@acme.dev");
    expect(screen.getByTestId("version-author-2")).toHaveTextContent("by the platform (generated)");
    expect(screen.getByTestId("version-author-1")).toHaveTextContent("author not recorded");

    const row = screen.getByTestId("version-row-3");
    await user.click(within(row).getByRole("button", { name: /Soft-delete version 3/ }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByTestId("step-up")).toHaveAttribute("data-phase", "intro");
    await user.click(within(dialog).getByTestId("step-up-send"));
    await user.type(await within(dialog).findByTestId("step-up-code"), "ABCDEFGHJK");
    await user.click(within(dialog).getByTestId("step-up-confirm"));

    await waitFor(() => expect(deleteSecret).toHaveBeenCalledTimes(2));
    expect(deleteSecret.mock.calls[1]?.[0]).toMatchObject({ environmentId: "cp-prod", name: "STRIPE_KEY", versions: [3] });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.queryByTestId("secret-action-error")).toBeNull();
  });

  it("a delete that fails for another reason says so instead of failing silently", async () => {
    const user = userEvent.setup();
    deleteSecret.mockRejectedValue(new ConnectError("the secret store denied this operation", Code.PermissionDenied));
    renderSection("STRIPE_KEY");
    const row = await screen.findByTestId("version-row-3");
    await user.click(within(row).getByRole("button", { name: /Soft-delete version 3/ }));
    expect(await screen.findByTestId("secret-action-error")).toHaveTextContent(/Deleting version 3 failed/);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("Confirm now steps up ahead of any change and the header starts counting down", async () => {
    const user = userEvent.setup();
    renderSection();
    await user.click(await screen.findByTestId("secrets-unlock"));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByTestId("step-up-send"));
    await user.type(await within(dialog).findByTestId("step-up-code"), "ABCDEFGHJK");
    await user.click(within(dialog).getByTestId("step-up-confirm"));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(await screen.findByTestId("secrets-step-up-countdown")).toHaveTextContent(/^1[45]:\d\d$/);
  });
});
