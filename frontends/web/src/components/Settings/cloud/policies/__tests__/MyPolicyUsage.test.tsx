/**
 * "Your usage" on the Spend limits card, over the real generated client on a
 * mock transport: the member's spend against their cap and the org's against
 * the org cap, in words; an unlimited member; the audit-mode note; and an
 * older control plane without GetMyPolicyUsage shown as "not available yet",
 * never as an error, while any other failure is.
 */
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { OrgPolicyMode, SpendAppliesTo, SpendPeriod } from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { renderWithQuery } from "@/test/renderWithQuery";

import { MyPolicyUsage, resetWords, usedPercent } from "../MyPolicyUsage";

const usd = (n: number) => BigInt(Math.round(n * 100)) * 10_000_000n;
const DEC1 = timestampFromDate(new Date("2026-12-01T00:00:00Z"));
const NOV1 = timestampFromDate(new Date("2026-11-01T00:00:00Z"));

/** A response to answer with, or an error to fail with. */
function renderUsage(answer: (() => object) | { error: ConnectError }) {
  const calls = vi.fn();
  setTransport(
    mockTransport({
      "OrgPolicyService.GetMyPolicyUsage":
        typeof answer === "function"
          ? (req: unknown) => {
              calls(req);
              return answer();
            }
          : answer,
    }),
  );
  renderWithQuery(<MyPolicyUsage orgId="org-a" orgName="Acme" />);
  return calls;
}

beforeEach(() => setTransport(null));

describe("MyPolicyUsage", () => {
  it("shows the caller's spend against their cap and the org's against its cap, for the org being looked at", async () => {
    const calls = renderUsage(() => ({
      policyVersion: 4n,
      mode: OrgPolicyMode.ENFORCE,
      enforced: true,
      appliesTo: SpendAppliesTo.MEMBERS_AND_APPS,
      member: { cap: { usdNanos: usd(50), period: SpendPeriod.MONTH }, spentUsdNanos: usd(12), periodStart: NOV1, periodEnd: DEC1 },
      org: { cap: { usdNanos: usd(400), period: SpendPeriod.MONTH }, spentUsdNanos: usd(380), periodStart: NOV1, periodEnd: DEC1 },
    }));
    const member = await screen.findByTestId("policy-usage-member");
    expect(member).toHaveTextContent("You've used $12 of your $50 monthly limit.");
    expect(member).toHaveTextContent("Resets Dec 1.");
    expect(within(member).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "24");

    const org = screen.getByTestId("policy-usage-org");
    expect(org).toHaveTextContent("Acme has used $380 of its $400 monthly limit, counting its hosted apps.");
    expect(within(org).getByRole("progressbar")).toHaveAttribute("aria-valuenow", "95");

    // Enforced: no audit note.
    expect(screen.queryByTestId("policy-usage-audit")).toBeNull();
    expect(calls).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org-a" }));
  });

  it("says an unlimited member has no personal limit, and notes audit mode", async () => {
    renderUsage(() => ({
      policyVersion: 2n,
      mode: OrgPolicyMode.AUDIT,
      enforced: false,
      member: { unlimited: true, spentUsdNanos: usd(7.5), periodStart: NOV1, periodEnd: DEC1 },
      org: { cap: { usdNanos: usd(100), period: SpendPeriod.DAY }, spentUsdNanos: usd(120) },
    }));
    expect(await screen.findByTestId("policy-usage-member")).toHaveTextContent("You've used $7.50 this month. You have no personal limit.");
    expect(screen.getByTestId("policy-usage-org")).toHaveTextContent("Acme has used $120 of its $100 daily limit.");
    expect(screen.getByTestId("policy-usage-org")).toHaveTextContent("Resets at midnight UTC.");
    expect(screen.getByTestId("policy-usage-audit")).toHaveTextContent("Over the limit: in audit mode that is counted, and nothing is refused.");
  });

  it("shows 'not available yet' for a control plane without the rpc", async () => {
    renderUsage({ error: new ConnectError("unimplemented", Code.Unimplemented) });
    expect(await screen.findByText("Your usage against these limits: not available yet.")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  // The server's refusals (control-plane memberCaller): each is shown in its
  // own words, never as "not available yet", and never as the server's prose.
  it.each([
    ["not_org_member", "You're not a member of this organization any more."],
    ["human_only", "Sign in as yourself to do this: a machine token cannot read or change policies."],
    ["", "Could not load your usage."],
  ])("shows a PermissionDenied refusal (reason %j) in its own words, with a retry", async (reason, words) => {
    renderUsage({ error: new ConnectError("server prose", Code.PermissionDenied, reason ? { "x-forge-error-reason": reason } : undefined) });
    const alert = await screen.findByRole("alert", undefined, { timeout: 5000 });
    expect(alert).toHaveTextContent(words);
    expect(alert).not.toHaveTextContent("server prose");
    expect(screen.queryByText(/not available yet/)).toBeNull();
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("usage arithmetic", () => {
  it("measures against the cap, with a zero cap full from the start", () => {
    expect(usedPercent(usd(12), usd(50))).toBe(24);
    expect(usedPercent(usd(60), usd(50))).toBe(120);
    expect(usedPercent(0n, 0n)).toBe(100);
  });

  it("says when the count starts again", () => {
    expect(resetWords({ usdNanos: 1n, period: SpendPeriod.DAY } as never, null)).toBe("Resets at midnight UTC.");
    expect(resetWords({ usdNanos: 1n, period: SpendPeriod.MONTH } as never, new Date("2026-12-01T00:00:00Z"))).toBe("Resets Dec 1.");
  });
});
