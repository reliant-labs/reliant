import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { MachinePricingReadinessCard, type MachinePricingTerms } from "../MachinePricingReadinessCard";
const terms: MachinePricingTerms = { pricingVersion: "candidate", activationReady: false, holdReasons: ["commissioning required"], termsSha256: "digest", monthlyPriceCents: 6900n, includedOwnSizeHours: 160n, includedDiskGib: 60n, size: "m", exactHourlyRateCents: "45/1", exactCostWeight: "1/1", routeId: "candidate-small", activationState: "held", effectivePeriodStart: { seconds: 1900000000n } };
const base = { canManage: true, onRetry: vi.fn(), onAccept: vi.fn() };
it("shows proposed held rates separately from current without a purchase action", () => {
  render(<MachinePricingReadinessCard {...base} terms={terms} currentPriceLabel="$20/month" />);
  expect(screen.getByText(/Current subscription: \$20/)).toBeInTheDocument();
  expect(screen.getByText(/Proposed m: \$69.00/)).toBeInTheDocument();
  expect(screen.getByText(/commissioning required/)).toBeInTheDocument();
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
});
it("requires owner/admin explicit digest consent and never announces activation", () => {
  const accept = vi.fn();
  render(<MachinePricingReadinessCard {...base} terms={{ ...terms, activationReady: true, holdReasons: [] }} onAccept={accept} />);
  expect(screen.getByRole("button", { name: /Accept next-period/ })).toBeDisabled();
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(screen.getByRole("button", { name: /Accept next-period/ }));
  expect(accept).toHaveBeenCalledWith({ pricingVersion: "candidate", termsSha256: "digest" });
  expect(screen.queryByText(/^Pricing is active/)).not.toBeInTheDocument();
});
it("refuses acceptance without next period or for members; pending is not activated", () => {
  const { rerender } = render(<MachinePricingReadinessCard {...base} terms={{ ...terms, activationReady: true, holdReasons: [], effectivePeriodStart: undefined }} />);
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  rerender(<MachinePricingReadinessCard {...base} canManage={false} terms={{ ...terms, activationReady: true, holdReasons: [] }} />);
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  rerender(<MachinePricingReadinessCard {...base} terms={terms} acceptanceState="pending" />);
  expect(screen.getByText(/Pending verification — not activated/)).toBeInTheDocument();
});
it("missing RPC or incomplete terms are unavailable, not old cheap plan fallback", () => {
  render(<MachinePricingReadinessCard {...base} error="Pricing readiness is unavailable on this server." />);
  expect(screen.getByRole("alert")).toHaveTextContent(/unavailable/);
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
});
