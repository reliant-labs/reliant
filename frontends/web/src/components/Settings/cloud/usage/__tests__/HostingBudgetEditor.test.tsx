import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { HostingBudgetEditor, parseBudgetCents } from "../HostingBudgetEditor";

const mocks = vi.hoisted(() => ({ client: {} as Record<string, unknown> }));
vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: () => mocks.client }));

const versions = [
  { version: 0n, infraBudgetCents: 5000n, actorUserId: "u1", actorEmail: "ada@example.com", policyVersion: "p1", source: "checkout_consent", agreementId: "a1", current: false },
  { version: 1n, infraBudgetCents: 8000n, actorUserId: "u2", actorEmail: "lin@example.com", policyVersion: "p1", source: "budget_change", agreementId: "a1", current: true },
];

function mount(props: Partial<Parameters<typeof HostingBudgetEditor>[0]> = {}) {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={cache}>
      <HostingBudgetEditor orgId="org-a" budgetCents={8000n} policyVersion="p1" canManage {...props} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mocks.client = {
    listUsageBillingBudgetVersions: vi.fn().mockResolvedValue({ versions }),
    setUsageBillingBudget: vi.fn().mockResolvedValue({}),
  };
});

describe("parseBudgetCents", () => {
  it("accepts positive dollars with at most two decimals", () => {
    expect(parseBudgetCents("12.34")).toBe(1234n);
    expect(parseBudgetCents(" 20 ")).toBe(2000n);
    expect(parseBudgetCents("0")).toBeNull();
    expect(parseBudgetCents("1.234")).toBeNull();
    expect(parseBudgetCents("12x")).toBeNull();
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
    expect(mocks.client.setUsageBillingBudget).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Save budget" }));
    await waitFor(() =>
      expect(mocks.client.setUsageBillingBudget).toHaveBeenCalledWith({
        orgId: "org-a",
        infraBudgetCents: 12000n,
        policyVersion: "p1",
        consent: true,
        expectedCurrentVersion: 1n,
      }),
    );
    expect(await screen.findByText(/Budget set to \$120.00 a month/)).toBeInTheDocument();
  });

  it("warns that lowering below this month's spend pauses deploys at once", async () => {
    mount({ spentCents: 6000 });
    await screen.findByTestId("hosting-budget-history");
    fireEvent.click(screen.getByTestId("hosting-budget-change"));
    fireEvent.change(screen.getByLabelText(/New monthly budget/), { target: { value: "40" } });
    expect(screen.getByTestId("hosting-budget-pauses-now")).toHaveTextContent("$60.00");
  });

  it("says when someone else changed it first", async () => {
    (mocks.client.setUsageBillingBudget as ReturnType<typeof vi.fn>).mockRejectedValue(new ConnectError("budget changed: expected version 1, current 2", Code.FailedPrecondition));
    mount();
    await screen.findByTestId("hosting-budget-history");
    fireEvent.click(screen.getByTestId("hosting-budget-change"));
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Save budget" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/changed since you opened this/);
  });

  it("is read-only, without a change button, before the server serves budget versions", async () => {
    mocks.client = {};
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
