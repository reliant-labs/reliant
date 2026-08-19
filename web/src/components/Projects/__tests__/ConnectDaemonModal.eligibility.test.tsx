/**
 * ProjectPicker's ConnectDaemonModal must not offer "Reliant Cloud" to a user
 * the server considers ineligible for compute.
 *
 * THE GAP THIS CLOSES: onboarding's ComputeStep gates its "Start cloud daemon"
 * button on `useCloudEligibility()` and, when ineligible, shows the reason plus
 * the coupon/plans affordances that are the way out. ProjectPicker's own
 * ConnectDaemonModal — reachable from the "Connect a daemon" / "Resume a
 * daemon" screens — consulted eligibility NOWHERE. It gated only on
 * `capabilities.cloudDaemons`, which is a deployment flag, not an entitlement.
 *
 * So an unfunded user who was ejected out of onboarding into the picker got a
 * fully-enabled "Provision a hosted daemon now" button. Clicking it fires
 * CreateDaemon. A brand-new production user reported exactly that, and it
 * succeeded.
 *
 * The server is the authority and its own gate is being fixed separately; this
 * is the client half — a UI that offers an action it has already been told will
 * be refused is a UI that teaches users the product is broken, and here it was
 * spending real money instead.
 */
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockCreateDaemon = vi.fn();
const mockUseCloudEligibility = vi.fn();

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCreateDaemon: (cbs: unknown) => {
    void cbs;
    return { mutateAsync: mockCreateDaemon, isPending: false };
  },
  useCloudEligibility: () => mockUseCloudEligibility(),
  useResumeDaemon: () => ({ mutate: vi.fn(), isPending: false, variables: undefined }),
}));

vi.mock("../../../services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true, managedCredits: true, gitConnections: true },
}));

vi.mock("../SelfHostedDaemonConnect", () => ({
  SelfHostedDaemonConnect: () => <div data-testid="self-hosted" />,
}));

// The coupon form and billing link are the ineligible user's way out; their
// internals are not under test here.
vi.mock("../../RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));

vi.mock("@/hooks/useGoToBilling", () => ({ useGoToBilling: () => vi.fn() }));

// The modal dynamically imports these inside startCloudDaemon. An empty list
// is the brand-new-user case, which is the one that reaches CreateDaemon.
const mockListDaemons = vi.fn();
vi.mock("../../../services/controlPlane/daemon", () => ({
  listDaemons: () => mockListDaemons(),
  hasActiveDaemon: (daemons: unknown[]) => daemons.length > 0,
  resumeDaemon: vi.fn(),
}));

vi.mock("../../ui/Modal", () => ({
  Modal: ({ isOpen, children }: { isOpen: boolean; children: React.ReactNode }) =>
    isOpen ? <div>{children}</div> : null,
}));

import { ConnectDaemonModal } from "../ConnectDaemonModal";

beforeEach(() => {
  vi.clearAllMocks();
  mockCreateDaemon.mockResolvedValue({});
  mockListDaemons.mockResolvedValue({ daemons: [] });
  mockUseCloudEligibility.mockReturnValue({
    eligible: true,
    reason: null,
    isLoading: false,
  });
});

describe("ConnectDaemonModal — cloud eligibility", () => {
  it("does NOT provision when the user is ineligible", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "Redeem a coupon code or choose a plan to start a cloud machine.",
      isLoading: false,
    });

    render(<ConnectDaemonModal isOpen onClose={vi.fn()} />);

    const cloud = screen.getByRole("button", { name: /Reliant Cloud/i });
    await userEvent.click(cloud);

    // The click must not reach CreateDaemon. This is the money assertion.
    await waitFor(() => {
      expect(mockCreateDaemon).not.toHaveBeenCalled();
    });
  });

  it("shows the ineligible user why, so the screen is not a dead end", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "Redeem a coupon code or choose a plan to start a cloud machine.",
      isLoading: false,
    });

    render(<ConnectDaemonModal isOpen onClose={vi.fn()} />);

    // The reason appears twice by design: once as the cloud card's own copy
    // and once in the explainer panel beneath it, which also carries the
    // coupon form and the plans link.
    expect(
      screen.getAllByText(/Redeem a coupon code or choose a plan/i).length,
    ).toBeGreaterThan(0);
    expect(screen.getByTestId("redeem-coupon")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /View plans/i })).toBeInTheDocument();
  });

  // Never block an entitled user — the failure mode of an over-eager gate is
  // just as bad as the one we're closing.
  it("still provisions for an eligible user", async () => {
    render(<ConnectDaemonModal isOpen onClose={vi.fn()} />);

    await userEvent.click(screen.getByRole("button", { name: /Reliant Cloud/i }));

    await waitFor(() => {
      expect(mockCreateDaemon).toHaveBeenCalled();
    });
  });

  // While eligibility is still loading we must not present an enabled button:
  // "not yet known" is not "allowed". (Loading is not the negative of success.)
  it("does not provision while eligibility is still loading", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: null,
      isLoading: true,
    });

    render(<ConnectDaemonModal isOpen onClose={vi.fn()} />);

    await userEvent.click(screen.getByRole("button", { name: /Reliant Cloud/i }));

    await waitFor(() => {
      expect(mockCreateDaemon).not.toHaveBeenCalled();
    });
  });
});
