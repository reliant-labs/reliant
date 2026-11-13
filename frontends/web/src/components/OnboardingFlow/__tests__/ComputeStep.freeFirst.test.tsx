/**
 * The free first run leads. 7 of 9 new users picked cloud, hit the payment
 * wall and left (onboarding-stalls-2026-11-12), so wherever the desktop app or
 * CLI can run, "Your own computer" is preselected and first, and the hosted
 * machines follow with their price read from the plan catalog. On a phone the
 * free option still leads and is the default, but pairing cannot be finished
 * there, so it shows laptop instructions and the copy-link / email handoff
 * instead of a download, and keeps waiting for the laptop to connect.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

import type { LaunchPlan } from "../types";

const NO_DAEMON = { daemons: [], activeDaemon: undefined, loading: false, refresh: vi.fn() };
const mockDaemons = vi.fn((): Record<string, unknown> => NO_DAEMON);
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => mockDaemons(),
}));
const mockEligible = vi.fn(() => false);
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCloudEligibility: () => ({
    eligible: mockEligible(),
    reason: null,
    isLoading: false,
    grantedSmallDaemonSecondsRemaining: 0,
    refetch: vi.fn(),
  }),
  computeIneligibleCopy: () => "",
}));
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({
    isLoading: false,
    data: {
      plans: [
        {
          id: "plan_compute_m",
          name: "M",
          displayOrder: 1,
          priceCents: 2000n,
          structuredLimits: {
            allowedDaemonSizes: ["m"],
            includedSmallDaemonHours: 17n,
          },
        },
      ],
    },
  }),
}));
vi.mock("@/components/RedeemCouponForm", () => ({
  RedeemCouponForm: () => <div data-testid="redeem-coupon" />,
}));
vi.mock("@/components/Machines/PairMachinePanel", () => ({
  PairMachinePanel: () => <div data-testid="self-hosted-connect" />,
}));
vi.mock("@/hooks/useBundledDaemonPending", () => ({
  useBundledDaemonPending: () => false,
}));
const mockIsPhone = vi.fn(() => false);
vi.mock("@/lib/mobileRedirect", () => ({ isPhoneViewport: () => mockIsPhone() }));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true, managedCredits: true, gitConnections: true },
}));
vi.mock("@/services/controlPlane/reliantAI", () => ({
  RedeemedCouponKind: { COMPUTE_MINUTES: 1, WALLET_CREDIT: 2 },
}));

import { ComputeStep } from "../steps/ComputeStep";

function renderStep(plan: Partial<LaunchPlan> = {}, updatePlan = vi.fn(async () => {})) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  render(
    <ComputeStep plan={plan} updatePlan={updatePlan} onNext={vi.fn()} onBack={vi.fn()} />,
    {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    },
  );
  return updatePlan;
}

const ownComputer = () => screen.getByRole("button", { name: /Your own computer/ });

beforeEach(() => {
  vi.clearAllMocks();
  mockIsPhone.mockReturnValue(false);
  mockEligible.mockReturnValue(false);
  mockDaemons.mockReturnValue(NO_DAEMON);
});

describe("ComputeStep — your own computer leads on a computer", () => {
  it("preselects it, first in the list, with the honest copy", () => {
    renderStep();

    const own = ownComputer();
    expect(own).toHaveAttribute("aria-pressed", "true");
    expect(own).toHaveTextContent("Free");
    expect(own).toHaveTextContent(/runs on your machine/i);
    expect(own).toHaveTextContent(/bring your own model key or subscription/i);
    expect(own).toHaveTextContent(/desktop app or CLI/);

    const group = screen.getByRole("group");
    expect(group.firstElementChild).toBe(own);
  });

  it("shows cloud second, with the price from the plan catalog and that it is paid", () => {
    renderStep();

    const toggle = screen.getByTestId("compute-cloud-toggle");
    expect(toggle).toHaveTextContent("Reliant Cloud machines · from $20/mo");
    expect(toggle).toHaveTextContent(/paid/i);
    expect(toggle).toHaveTextContent(/keeps running after you close your laptop/i);
    expect(ownComputer().compareDocumentPosition(toggle)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
  });

  it("says nothing is paid for until a hosted machine is picked: no cloud CTA", () => {
    renderStep();
    expect(screen.queryByRole("button", { name: /^Continue with/ })).toBeNull();
  });

  it("in a browser with no daemon, leads straight into the connect flow with a wait state", () => {
    renderStep();

    expect(screen.getByTestId("self-hosted-connect")).toBeVisible();
    expect(screen.getByText(/looking for your computer/i)).toBeInTheDocument();
  });

  it("does not record a compute choice until the machine connects", () => {
    const updatePlan = renderStep();
    expect(updatePlan).not.toHaveBeenCalled();
  });

  it("keeps a hosted choice the user already made (Back from checkout)", () => {
    renderStep({ compute: "cloud_paid", computePlanId: "plan_compute_m" });

    expect(ownComputer()).toHaveAttribute("aria-pressed", "false");
    expect(screen.queryByTestId("compute-cloud-toggle")).toBeNull();
    expect(screen.getByText("M")).toBeVisible();
  });

  it("keeps a size picked before a reload, even before compute is committed", () => {
    renderStep({ computePlanId: "plan_compute_m" });
    expect(ownComputer()).toHaveAttribute("aria-pressed", "false");
  });

  it("lets the user pick cloud from the folded list", () => {
    const updatePlan = renderStep();
    fireEvent.click(screen.getByTestId("compute-cloud-toggle"));
    fireEvent.click(screen.getByText("M"));

    expect(updatePlan).toHaveBeenCalledWith({ computePlanId: "plan_compute_m" });
    expect(ownComputer()).toHaveAttribute("aria-pressed", "false");
  });
});

describe("ComputeStep — on a phone", () => {
  beforeEach(() => mockIsPhone.mockReturnValue(true));

  it("shows your own computer first and preselected, with cloud second and marked paid", () => {
    renderStep();

    const own = ownComputer();
    expect(own).toHaveAttribute("aria-pressed", "true");
    expect(own).toHaveTextContent("Free");
    expect(screen.getByRole("group").firstElementChild).toBe(own);

    const toggle = screen.getByTestId("compute-cloud-toggle");
    expect(toggle).toHaveTextContent(/from \$20\/mo/);
    expect(toggle).toHaveTextContent(/paid/i);
    expect(own.compareDocumentPosition(toggle)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
  });

  it("gives laptop instructions instead of a pairing panel, download link or install button", () => {
    renderStep();

    expect(screen.queryByTestId("self-hosted-connect")).toBeNull();
    const steps = screen.getByTestId("compute-step-phone-local");
    expect(steps).toHaveTextContent(/On your laptop, open .+ and sign in\./);
    expect(steps).toHaveTextContent(/Install the Reliant desktop app \(or run the CLI command shown there\)\./);
    expect(steps).toHaveTextContent(/Your computer connects to this account\. Come back here, or keep going on the laptop\./);
    expect(steps).toHaveTextContent(window.location.origin);
    expect(steps.querySelectorAll("li")).toHaveLength(3);
    expect(screen.queryByRole("link", { name: /download/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /download|install/i })).toBeNull();
  });

  it("does not dead-end: waits for the computer and keeps the handoff visible", () => {
    renderStep();

    expect(screen.getByRole("button", { name: /Waiting for your computer to connect/ })).toBeDisabled();
    expect(screen.getByText(/looking for your computer/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Continue on your computer: copy link/ })).toBeVisible();
    expect(screen.getByRole("link", { name: /Email it to myself/ })).toBeVisible();
  });

  it("copies the link as the action, and the email link carries it", async () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    renderStep();

    fireEvent.click(screen.getByRole("button", { name: /Continue on your computer/ }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(window.location.href));
    expect(await screen.findByText("Link copied")).toBeInTheDocument();

    const mail = screen.getByRole("link", { name: /Email it to myself/ });
    expect(mail.getAttribute("href")).toMatch(/^mailto:\?subject=.*&body=/);
  });

  it("reports a failed copy and points at the email link", async () => {
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText: vi.fn(async () => { throw new Error("denied"); }) },
      configurable: true,
    });
    renderStep();

    fireEvent.click(screen.getByRole("button", { name: /Continue on your computer/ }));
    expect(await screen.findByText(/couldn.t copy/i)).toBeInTheDocument();
  });

  it("advances by itself once the laptop's machine connects to the account", async () => {
    const onNext = vi.fn();
    mockDaemons.mockReturnValue({
      daemons: [{ status: 1 } as never],
      activeDaemon: { status: 1 } as never,
      loading: false,
      refresh: vi.fn(),
    });
    const updatePlan = vi.fn(async () => {});
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    render(
      <QueryClientProvider client={client}>
        <ComputeStep plan={{}} updatePlan={updatePlan} onNext={onNext} onBack={vi.fn()} />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(onNext).toHaveBeenCalled());
    expect(updatePlan).toHaveBeenCalledWith(expect.objectContaining({ compute: "local_daemon" }));
  });

  it("switches to cloud when a hosted row is picked, and the instructions go away", () => {
    const updatePlan = renderStep();
    fireEvent.click(screen.getByTestId("compute-cloud-toggle"));
    fireEvent.click(screen.getByText("M"));

    expect(updatePlan).toHaveBeenCalledWith({ computePlanId: "plan_compute_m" });
    expect(screen.queryByTestId("compute-step-phone-local")).toBeNull();
    expect(ownComputer()).toHaveAttribute("aria-pressed", "false");
  });

  it("keeps a hosted choice already in the plan (Back from checkout)", () => {
    renderStep({ compute: "cloud_paid", computePlanId: "plan_compute_m" });
    expect(ownComputer()).toHaveAttribute("aria-pressed", "false");
    expect(screen.queryByTestId("compute-step-phone-local")).toBeNull();
  });
});
