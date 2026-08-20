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
  // The ineligible user is not offered the tile at all any more, which is a
  // stronger guarantee than the old disabled one: there is no cloud control to
  // click, so no click can reach CreateDaemon. (It used to render greyed out,
  // which made a dead control the first thing on the screen and pushed the
  // working ones — coupon, billing — into a panel underneath.)
  it("does NOT offer a cloud tile, and never provisions, when the user is ineligible", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: "Redeem a coupon code or choose a plan to start a cloud machine.",
      isLoading: false,
    });

    render(<ConnectDaemonModal isOpen onClose={vi.fn()} />);

    expect(
      screen.queryByRole("button", { name: /Reliant Cloud/i }),
    ).not.toBeInTheDocument();

    // Nothing on the screen can start a machine. This is the money assertion.
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

    // The panel that replaces the tile carries the reason plus both ways out.
    expect(
      screen.getByText(/Redeem a coupon code or choose a plan/i),
    ).toBeInTheDocument();
    expect(screen.getByTestId("redeem-coupon")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Set up billing/i }),
    ).toBeEnabled();
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

  // While eligibility is still loading we must not present a cloud control:
  // "not yet known" is not "allowed". (Loading is not the negative of success.)
  //
  // Loading is also NOT the ineligible state — showing "you have no plan" to a
  // user whose plan simply has not loaded yet would be a lie that resolves
  // itself a moment later, so neither the tile nor the blocked panel renders.
  it("offers no cloud control at all while eligibility is still loading", async () => {
    mockUseCloudEligibility.mockReturnValue({
      eligible: false,
      reason: null,
      isLoading: true,
    });

    render(<ConnectDaemonModal isOpen onClose={vi.fn()} />);

    expect(
      screen.queryByRole("button", { name: /Reliant Cloud/i }),
    ).not.toBeInTheDocument();
    expect(screen.queryByTestId("redeem-coupon")).not.toBeInTheDocument();

    // Self-hosted is always available and is unaffected by eligibility.
    expect(
      screen.getByRole("button", { name: /Self-hosted/i }),
    ).toBeEnabled();

    await waitFor(() => {
      expect(mockCreateDaemon).not.toHaveBeenCalled();
    });
  });
});
