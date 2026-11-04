import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, expect, it, vi } from "vitest";
import { MachinePlanChange } from "../MachinePlanChange";

const mock = vi.hoisted(() => ({ preview: vi.fn(), change: vi.fn(), active: { id: "org-a", role: "owner" } }));
vi.mock("@/hooks/useOrgs", () => ({ useActiveOrg: () => ({ active: mock.active }) }));
vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({ previewMachinePlanChange: mock.preview, changeMachinePlan: mock.change }),
}));

const renewal = { seconds: 1_900_000_000n, nanos: 0 };
const upgrade = {
  fromPlanId: "plan_compute_m", toPlanId: "plan_compute_l", pricingVersion: "machine-pooled-v1", direction: "upgrade",
  prorationDate: { seconds: 1_899_000_000n, nanos: 0 }, periodEnd: renewal, amountDueTodayCents: 1500n, prorationCents: 1500n,
  currentMonthlyPriceCents: 3900n, newMonthlyPriceCents: 6900n, includedHoursDelta: "80.0", newSize: "l", newIncludedHours: 160n,
  allowed: true, blockedReasons: [], blockingMachines: [], termsSha256: "a".repeat(64), effectiveAt: { seconds: 1_899_000_000n, nanos: 0 }, scheduledPlanId: "",
};

beforeEach(() => {
  vi.clearAllMocks();
  mock.active = { id: "org-a", role: "owner" };
  mock.preview.mockResolvedValue(upgrade);
  mock.change.mockResolvedValue({ changeId: "chg", state: "applied", planId: "plan_compute_l", direction: "upgrade", amountDueTodayCents: 1500n });
});

function mount(planId = "plan_compute_l") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const done = vi.fn();
  render(
    <QueryClientProvider client={client}>
      <MachinePlanChange planId={planId} onDone={done} onCancel={vi.fn()} />
    </QueryClientProvider>,
  );
  return { done };
}

it("shows today's prorated charge and the new monthly price, then confirms the exact quote", async () => {
  mount();
  expect(await screen.findByTestId("plan-change-due-today")).toHaveTextContent("$15.00");
  expect(screen.getByText(/\$69\.00\/month from/)).toHaveTextContent("(now $39.00)");
  expect(screen.getByText(/\+80\.0 L hours/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Upgrade and pay $15.00" }));
  await waitFor(() =>
    expect(mock.change).toHaveBeenCalledWith({
      orgId: "org-a", planId: "plan_compute_l", pricingVersion: "machine-pooled-v1",
      prorationDate: upgrade.prorationDate, termsSha256: upgrade.termsSha256,
    }),
  );
  expect(await screen.findByText(/You're on L now\. \$15\.00 was charged\./)).toBeInTheDocument();
});

it("a downgrade charges nothing today and says when it takes effect", async () => {
  mock.preview.mockResolvedValue({ ...upgrade, direction: "downgrade", toPlanId: "plan_compute_s1", newSize: "s1", amountDueTodayCents: 0n, prorationCents: 0n, newMonthlyPriceCents: 2500n, effectiveAt: renewal, includedHoursDelta: "0" });
  mount("plan_compute_s1");
  expect(await screen.findByText(/Switch to S1 at your next billing period/)).toBeInTheDocument();
  expect(screen.getByTestId("plan-change-due-today")).toHaveTextContent("$0.00");
  expect(screen.getByRole("button", { name: /^Switch to S1 on / })).toBeEnabled();
});

it("a blocked downgrade lists the machines to resize and cannot be confirmed", async () => {
  mock.preview.mockResolvedValue({ ...upgrade, direction: "downgrade", newSize: "s1", allowed: false, blockedReasons: ["resize or delete 1 machine(s) larger than the S1 plan first"], blockingMachines: [{ daemonId: "d1", name: "big-one", size: "l" }] });
  mount("plan_compute_s1");
  expect(await screen.findByTestId("plan-change-blocking-machines")).toHaveTextContent("big-one (L)");
  expect(screen.getByRole("button", { name: /^Switch to S1/ })).toBeDisabled();
  expect(mock.change).not.toHaveBeenCalled();
});

it("a member is told who can change the plan and never requests a quote", async () => {
  mock.active = { id: "org-a", role: "member" };
  mount();
  expect(await screen.findByText(/Only an organization owner or admin/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /Upgrade and pay/ })).not.toBeInTheDocument();
  expect(mock.preview).not.toHaveBeenCalled();
});

it("a scheduled downgrade can be withdrawn by keeping the current plan", async () => {
  mock.preview.mockImplementation(({ planId }: { planId: string }) =>
    Promise.resolve(
      planId === "plan_compute_m"
        ? { ...upgrade, direction: "cancel_downgrade", toPlanId: "plan_compute_m", newSize: "m", amountDueTodayCents: 0n, scheduledPlanId: "plan_compute_s1" }
        : { ...upgrade, allowed: false, blockedReasons: ["cancel the downgrade already scheduled for your next billing period first"], fromPlanId: "plan_compute_m", scheduledPlanId: "plan_compute_s1" },
    ),
  );
  mock.change.mockResolvedValue({ changeId: "chg", state: "applied", planId: "plan_compute_m", direction: "cancel_downgrade", amountDueTodayCents: 0n });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Cancel the scheduled switch" }));
  fireEvent.click(await screen.findByRole("button", { name: "Keep my current plan" }));
  await waitFor(() => expect(mock.change).toHaveBeenCalledWith(expect.objectContaining({ planId: "plan_compute_m" })));
  expect(await screen.findByText("Your plan stays as it is.")).toBeInTheDocument();
});

it("an answer lost in transit says the change settles on its own, never to pay again", async () => {
  mock.change.mockResolvedValue({ changeId: "chg", state: "confirming", planId: "plan_compute_l", direction: "upgrade", amountDueTodayCents: 1500n });
  const { done } = mount();
  fireEvent.click(await screen.findByRole("button", { name: /Upgrade and pay/ }));
  expect(await screen.findByText(/you won't be charged twice/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Done" }));
  expect(done).toHaveBeenCalled();
});

it("a confirmed change stays reported when the refreshed quote then fails", async () => {
  const { done } = mount();
  fireEvent.click(await screen.findByRole("button", { name: "Upgrade and pay $15.00" }));
  // The confirmation invalidates the quote; the plan is now L, so the server refuses to quote it.
  mock.preview.mockRejectedValue(Object.assign(new Error("[failed_precondition]"), { rawMessage: "You are already on this plan." }));
  expect(await screen.findByText(/You're on L now\. \$15\.00 was charged\./)).toBeInTheDocument();
  await waitFor(() => expect(mock.preview).toHaveBeenCalledTimes(2));
  expect(screen.queryByText("You are already on this plan.")).not.toBeInTheDocument();
  expect(screen.getByText(/You're on L now/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Done" }));
  expect(done).toHaveBeenCalled();
});

it("a transport failure after the server may have charged never claims nothing was charged, and retries the same quote", async () => {
  mock.change.mockRejectedValueOnce(new ConnectError("fetch failed", Code.Unavailable));
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Upgrade and pay $15.00" }));
  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent(/may or may not have gone through/);
  expect(alert).not.toHaveTextContent(/Nothing was charged/i);
  expect(screen.queryByRole("button", { name: /Upgrade and pay/ })).not.toBeInTheDocument();
  // The server's quote moves on meanwhile; the retry still sends the confirmed one.
  mock.preview.mockResolvedValue({ ...upgrade, termsSha256: "b".repeat(64), amountDueTodayCents: 1600n });
  fireEvent.click(screen.getByRole("button", { name: "Check again" }));
  expect(await screen.findByText(/You're on L now\. \$15\.00 was charged\./)).toBeInTheDocument();
  expect(mock.change).toHaveBeenCalledTimes(2);
  expect(mock.change).toHaveBeenLastCalledWith(expect.objectContaining({ termsSha256: upgrade.termsSha256 }));
});

it("a declined payment keeps the panel open with the server's message", async () => {
  mock.change.mockRejectedValue(Object.assign(new Error("[failed_precondition] declined"), { rawMessage: "Your card was declined. Nothing was charged and your plan is unchanged." }));
  mount();
  fireEvent.click(await screen.findByRole("button", { name: /Upgrade and pay/ }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Your card was declined. Nothing was charged");
  expect(screen.getByRole("button", { name: /Upgrade and pay/ })).toBeEnabled();
});
