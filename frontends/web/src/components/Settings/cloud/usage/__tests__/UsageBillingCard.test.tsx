import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { UsageBillingCard, type UsageBillingCardProps } from "../UsageBillingCard";

const base: UsageBillingCardProps = { enabled: true, policyVersion: "policy-1", canManage: true, rates: ["CPU: catalog rate"], onRetry: vi.fn(), onCheckout: vi.fn() };
describe("independent hosting consent", () => {
  it("fails closed when disabled, with no machine checkout", () => {
    render(<UsageBillingCard {...base} enabled={false} />);
    expect(screen.getByText(/Buying a machine plan does not enable it/)).toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });
  it("requires explicit consent and an exact positive budget", async () => {
    const checkout = vi.fn();
    render(<UsageBillingCard {...base} onCheckout={checkout} />);
    fireEvent.change(screen.getByLabelText(/Monthly app hosting admission budget/), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: /Authorize and continue/ }));
    await screen.findAllByRole("alert");
    expect(checkout).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText(/Monthly app hosting admission budget/), { target: { value: "12.34junk" } });
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: /Authorize and continue/ }));
    await waitFor(() => expect(screen.getByText(/at most two decimal/)).toBeInTheDocument());
    expect(checkout).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText(/Monthly app hosting admission budget/), { target: { value: "12.34" } });
    fireEvent.click(screen.getByRole("button", { name: /Authorize and continue/ }));
    await waitFor(() => expect(checkout).toHaveBeenCalledWith({ policyVersion: "policy-1", infraBudgetCents: 1234n }));
    expect(screen.getByText(/isn’t a hard limit on your bill/)).toBeInTheDocument();
  });
  it("starts a new agreement at the $50 default budget, which consent submits unchanged", async () => {
    const checkout = vi.fn();
    render(<UsageBillingCard {...base} onCheckout={checkout} />);
    expect(screen.getByLabelText(/Monthly app hosting admission budget/)).toHaveValue("50.00");
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: /Authorize and continue/ }));
    await waitFor(() => expect(checkout).toHaveBeenCalledWith({ policyVersion: "policy-1", infraBudgetCents: 5000n }));
  });
  it("states the price list and the minimum in plain words, never a base-fee claim", () => {
    render(<UsageBillingCard {...base} rates={["Hosted CPU: $0.07/vCPU-hr"]} minimumLine="A $5 minimum applies in any month with hosting usage." />);
    expect(screen.getByTestId("hosting-rates")).toHaveTextContent("Hosted CPU: $0.07/vCPU-hr");
    expect(screen.getByTestId("hosting-minimum")).toHaveTextContent("A $5 minimum applies");
    expect(screen.getByRole("checkbox")).toHaveAccessibleName(/including the monthly minimum/);
    expect(document.body.textContent).not.toMatch(/no base fee|policy-1|under policy/i);
  });
  it("does not treat pending checkout as active funding", () => {
    render(<UsageBillingCard {...base} agreement={{ status: "pending", infraBudgetCents: 1000n }} />);
    expect(screen.getByRole("status")).toHaveTextContent(/not active until the server verifies/);
    expect(screen.getByRole("button", { name: "Check activation status" })).toBeInTheDocument();
    expect(screen.getByLabelText(/Monthly app hosting admission budget/)).toHaveValue("10.00");
    expect(screen.getByLabelText(/Monthly app hosting admission budget/)).toHaveAttribute("readonly");
  });
  it("does not offer consent to members or when rates are unknown", () => {
    const { rerender } = render(<UsageBillingCard {...base} canManage={false} />);
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    rerender(<UsageBillingCard {...base} rates={[]} />);
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.getByText(/rates or policy are unavailable/)).toBeInTheDocument();
  });
  it("renders server-verified active agreement independently of machines", () => {
    render(<UsageBillingCard {...base} agreement={{ status: "active", infraBudgetCents: 2500n }} />);
    expect(screen.getByRole("status")).toHaveTextContent("App hosting billing is active.");
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });
});
