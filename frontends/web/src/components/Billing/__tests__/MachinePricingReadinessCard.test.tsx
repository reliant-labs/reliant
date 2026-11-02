import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { MachinePricingReadinessCard, type MachinePricingTerms } from "../MachinePricingReadinessCard";
const terms: MachinePricingTerms = { pricingVersion: "candidate", activationReady: false, holdReasons: ["commissioning required"], termsSha256: "digest", monthlyPriceCents: 6900n, includedOwnSizeHours: 160n, includedDiskGib: 60n, size: "m", exactHourlyRateCents: "45/1", exactCostWeight: "1/1", routeId: "candidate-small", activationState: "held", effectivePeriodStart: { seconds: 1900000000n } };
const ready = { ...terms, activationReady: true, holdReasons: [] };
const base = { canManage: true, onRetry: vi.fn(), onAccept: vi.fn() };
it("renders nothing for a held proposal: the customer cannot act on operator state", () => {
  const { container } = render(<MachinePricingReadinessCard {...base} terms={terms} currentPriceLabel="$20" />);
  expect(container).toBeEmptyDOMElement();
  expect(screen.queryByText(/commissioning|proposed|approved live offer|route|version/i)).not.toBeInTheDocument();
});
it("states the next-period terms plainly, beside the current price", () => {
  render(<MachinePricingReadinessCard {...base} terms={ready} currentPriceLabel="$20" />);
  expect(screen.getByTestId("next-period-summary")).toHaveTextContent(/M machine · \$69\/month · 160 hours included · 60 GB disk · \$0\.45\/hour after that/);
  expect(screen.getByText(/You pay \$20\/month now/)).toBeInTheDocument();
  expect(document.body.textContent).not.toMatch(/candidate|route|own-size|GiB|equivalent|not an approved live offer|billing hold/i);
});
it("requires owner/admin explicit consent to the stated terms and never announces activation", () => {
  const accept = vi.fn();
  render(<MachinePricingReadinessCard {...base} terms={ready} onAccept={accept} />);
  expect(screen.getByRole("checkbox")).toHaveAccessibleName(/I agree to pay \$69 a month for the M machine plan.*160 hours.*\$0\.45 an hour.*renews monthly.*cancel anytime/);
  expect(screen.getByRole("button", { name: /Confirm pricing/ })).toBeDisabled();
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(screen.getByRole("button", { name: /Confirm pricing/ }));
  expect(accept).toHaveBeenCalledWith({ pricingVersion: "candidate", termsSha256: "digest" });
  expect(screen.queryByText(/^Pricing is active/)).not.toBeInTheDocument();
});
it("refuses acceptance without a next period or for members; a recorded confirmation is not activation", () => {
  const { rerender, container } = render(<MachinePricingReadinessCard {...base} terms={{ ...ready, effectivePeriodStart: undefined }} />);
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  rerender(<MachinePricingReadinessCard {...base} canManage={false} terms={ready} />);
  expect(container).toBeEmptyDOMElement();
  rerender(<MachinePricingReadinessCard {...base} terms={ready} acceptanceState="pending" />);
  expect(screen.getByRole("status")).toHaveTextContent(/confirmation is recorded/);
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
});
it("missing or incomplete terms render nothing rather than a zero-price summary", () => {
  const { container, rerender } = render(<MachinePricingReadinessCard {...base} />);
  expect(container).toBeEmptyDOMElement();
  rerender(<MachinePricingReadinessCard {...base} terms={{ ...ready, monthlyPriceCents: 0n }} />);
  expect(container).toBeEmptyDOMElement();
});
it("a failed confirmation says so beside the terms", () => {
  render(<MachinePricingReadinessCard {...base} terms={ready} error="We couldn't record your confirmation. Try again." />);
  expect(screen.getByRole("alert")).toHaveTextContent(/couldn't record/);
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
});
