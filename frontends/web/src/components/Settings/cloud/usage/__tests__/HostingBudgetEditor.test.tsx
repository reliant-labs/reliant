import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { billingHandlers } from "@/test/billingTransport";

import { HostingBudgetEditor } from "../HostingBudgetEditor";
import { validateHostingBudget } from "../hostingBudget";
import { hostingBillingKeys } from "../hostingBillingQueries";

const listUsageBillingBudgetVersions = vi.fn();
const setUsageBillingBudget = vi.fn();

const versions = [
  { version: 0n, infraBudgetCents: 5000n, actorUserId: "u1", actorEmail: "ada@example.com", policyVersion: "p1", source: "checkout_consent", agreementId: "a1", current: false },
  { version: 1n, infraBudgetCents: 8000n, actorUserId: "u2", actorEmail: "lin@example.com", policyVersion: "p1", source: "budget_change", agreementId: "a1", current: true },
];

function mount(props: Partial<Parameters<typeof HostingBudgetEditor>[0]> = {}) {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return { cache, ...mount.render(cache, props) };
}
mount.render = (cache: QueryClient, props: Partial<Parameters<typeof HostingBudgetEditor>[0]>) =>
  render(
    <QueryClientProvider client={cache}>
      <HostingBudgetEditor orgId="org-a" budgetCents={8000n} policyVersion="p1" canManage {...props} />
    </QueryClientProvider>,
  );

beforeEach(() => {
  listUsageBillingBudgetVersions.mockReset().mockResolvedValue({ versions });
  setUsageBillingBudget.mockReset().mockResolvedValue({});
  setTransport(mockTransport(billingHandlers({ listUsageBillingBudgetVersions, setUsageBillingBudget })));
});
afterEach(() => setTransport(null));

describe("validateHostingBudget", () => {
  it("accepts positive dollars with at most two decimals, up to $10,000 inclusive", () => {
    expect(validateHostingBudget("12.34").cents).toBe(1234n);
    expect(validateHostingBudget(" 20 ").cents).toBe(2000n);
    expect(validateHostingBudget("10000.00").cents).toBe(1_000_000n);
    expect(validateHostingBudget("10000.01").error).toMatch(/\$10,000 or less/);
    expect(validateHostingBudget("0").error).toMatch(/above \$0/);
    expect(validateHostingBudget("1.234").error).toMatch(/two decimal places/);
    expect(validateHostingBudget("12x").error).toMatch(/positive dollar amount/);
  });
});

describe("HostingBudgetEditor", () => {
  it("shows every version, newest first, with who set it", async () => {
    mount();
    const history = await screen.findByTestId("hosting-budget-history");
    const items = history.querySelectorAll("li");
    expect(items[0]).toHaveTextContent("$80.00");
    expect(items[0]).toHaveTextContent("current");
    expect(items[0]).toHaveTextContent("Changed by lin@example.com");
    expect(items[1]).toHaveTextContent("Set at checkout by ada@example.com");
  });

  it("requires consent, then saves with the policy and the version the user saw", async () => {
    mount();
    await screen.findByTestId("hosting-budget-history");
    fireEvent.click(screen.getByTestId("hosting-budget-change"));
    fireEvent.change(screen.getByLabelText(/New monthly budget/), { target: { value: "120" } });
    fireEvent.click(screen.getByRole("button", { name: "Save budget" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/Agree to the usage billing policy/);
    expect(setUsageBillingBudget).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Save budget" }));
    await waitFor(() =>
      expect(setUsageBillingBudget).toHaveBeenCalledWith(expect.objectContaining({
        orgId: "org-a",
        infraBudgetCents: 12000n,
        policyVersion: "p1",
        consent: true,
        expectedCurrentVersion: 1n,
      })),
    );
    expect(await screen.findByText(/Budget set to \$120.00 a month/)).toBeInTheDocument();
  });

  it("keeps a larger stored budget readable but requires edits to be $10,000 or less", async () => {
    mount({ budgetCents: 1_000_001n });
    expect(screen.getByText("$10,000.01")).toBeInTheDocument();
    await screen.findByTestId("hosting-budget-history");
    fireEvent.click(screen.getByTestId("hosting-budget-change"));
    const input = screen.getByLabelText(/New monthly budget/);
    expect(input).toHaveValue("10000.01");
    expect(input).toHaveAccessibleDescription(/\$10,000 per month/);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Save budget" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/\$10,000 or less/);
    expect(setUsageBillingBudget).not.toHaveBeenCalled();

    fireEvent.change(input, { target: { value: "10000.00" } });
    fireEvent.click(screen.getByRole("button", { name: "Save budget" }));
    await waitFor(() => expect(setUsageBillingBudget).toHaveBeenCalledWith(expect.objectContaining({ infraBudgetCents: 1_000_000n })));
  });

  it("warns that lowering below this month's spend pauses deploys at once", async () => {
    mount({ spentCents: 6000 });
    await screen.findByTestId("hosting-budget-history");
    fireEvent.click(screen.getByTestId("hosting-budget-change"));
    fireEvent.change(screen.getByLabelText(/New monthly budget/), { target: { value: "40" } });
    expect(screen.getByTestId("hosting-budget-pauses-now")).toHaveTextContent("$60.00");
  });

  it("says when someone else changed it first", async () => {
    setUsageBillingBudget.mockRejectedValue(new ConnectError("budget changed: expected version 1, current 2", Code.FailedPrecondition));
    mount();
    await screen.findByTestId("hosting-budget-history");
    fireEvent.click(screen.getByTestId("hosting-budget-change"));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Save budget" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/changed since you opened this/);
  });

  it("keeps the version the edit started from when the history refreshes mid-edit", async () => {
    const { cache } = mount();
    await screen.findByTestId("hosting-budget-history");
    fireEvent.click(screen.getByTestId("hosting-budget-change"));
    // Another admin changes the budget; this form's history refreshes to version 2.
    await act(async () => {
      cache.setQueryData(hostingBillingKeys.budgetVersions("org-a"), {
        versions: [
          ...versions.map((v) => ({ ...v, current: false })),
          { version: 2n, infraBudgetCents: 9000n, actorUserId: "u3", actorEmail: "max@example.com", policyVersion: "p1", source: "budget_change", agreementId: "a1", current: true },
        ],
      });
    });
    fireEvent.change(screen.getByLabelText(/New monthly budget/), { target: { value: "120" } });
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Save budget" }));
    await waitFor(() => expect(setUsageBillingBudget).toHaveBeenCalledWith(expect.objectContaining({ expectedCurrentVersion: 1n })));
  });

  it("offers no change until the history has loaded, so a save always carries its version", async () => {
    let resolve!: (v: unknown) => void;
    listUsageBillingBudgetVersions.mockReturnValue(new Promise((r) => { resolve = r; }));
    mount();
    const change = screen.getByTestId("hosting-budget-change");
    expect(change).toBeDisabled();
    fireEvent.click(change);
    expect(screen.queryByLabelText(/New monthly budget/)).not.toBeInTheDocument();
    await act(async () => resolve({ versions }));
    await waitFor(() => expect(screen.getByTestId("hosting-budget-change")).toBeEnabled());
    expect(setUsageBillingBudget).not.toHaveBeenCalled();
  });

  it("is read-only, without a change button, before the server serves budget versions", async () => {
    setTransport(mockTransport());
    mount();
    expect(await screen.findByTestId("hosting-budget-unserved")).toBeInTheDocument();
    expect(screen.queryByTestId("hosting-budget-change")).not.toBeInTheDocument();
    expect(screen.getByText("$80.00")).toBeInTheDocument();
  });

  it("offers no change to members", async () => {
    mount({ canManage: false });
    await screen.findByTestId("hosting-budget-history");
    expect(screen.queryByTestId("hosting-budget-change")).not.toBeInTheDocument();
    expect(screen.getByText(/Only an organization owner or admin/)).toBeInTheDocument();
  });
});
