/**
 * Policies › Denials, over the real generated client on a mock transport:
 * counts summed per policy and reason over the window, an audit-mode rule's
 * counts labelled "would have been refused" and an enforced one's "refused",
 * the members (and hosted apps) they acted as, the window the call asks for,
 * an empty window, an older control plane, and a refusal in its own words.
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { renderWithQuery } from "@/test/renderWithQuery";

import { PolicyDenials, reasonWords, summarizeDenials } from "../PolicyDenials";

const NAMES: Record<string, string> = { u_ada: "Ada", u_bob: "Bob", u_cy: "Cy", u_di: "Di" };
const nameOf = (id: string) => NAMES[id] ?? "a former member";

const COUNTS = [
  { day: "2026-11-14", policy: "models", reason: "model_not_allowed", userId: "u_ada", outcome: "refused", count: 20n },
  { day: "2026-11-13", policy: "models", reason: "model_not_allowed", userId: "u_bob", outcome: "refused", count: 11n },
  { day: "2026-11-14", policy: "spend", reason: "member_spend_cap_reached", userId: "u_ada", outcome: "audited", count: 4n },
  { day: "2026-11-13", policy: "spend", reason: "member_spend_cap_reached", userId: "u_bob", outcome: "audited", count: 3n },
  { day: "2026-11-12", policy: "spend", reason: "member_spend_cap_reached", userId: "u_cy", outcome: "audited", count: 1n },
  { day: "2026-11-12", policy: "spend", reason: "member_spend_cap_reached", userId: "u_di", outcome: "audited", count: 1n },
  { day: "2026-11-12", policy: "spend", reason: "org_spend_cap_reached", userId: "", outcome: "audited", count: 2n },
];

function setup(answer: ((req: unknown) => object) | { error: ConnectError }) {
  const calls = vi.fn();
  setTransport(
    mockTransport({
      "OrgPolicyService.ListOrgPolicyDenials":
        typeof answer === "function"
          ? (req: unknown) => {
              calls(req);
              return answer(req);
            }
          : answer,
    }),
  );
  renderWithQuery(<PolicyDenials orgId="org-a" nameOf={nameOf} />);
  return calls;
}

beforeEach(() => setTransport(null));

describe("PolicyDenials", () => {
  it("sums each policy's counts per reason, and labels audit-mode counts 'would have been refused'", async () => {
    const calls = setup(() => ({ counts: COUNTS, refusedTotal: 31n, auditedTotal: 11n }));
    expect(await screen.findByTestId("policy-denials-totals")).toHaveTextContent(
      "31 calls refused this week; 11 calls would have been refused by rules in audit mode.",
    );

    const models = screen.getByTestId("policy-denial-models-model_not_allowed-refused");
    expect(models).toHaveTextContent("Models");
    expect(within(models).getByText("Enforced")).toBeInTheDocument();
    expect(models).toHaveTextContent("A model the policy does not allow · Ada and Bob");
    expect(models).toHaveTextContent("31 calls refused");

    const spend = screen.getByTestId("policy-denial-spend-member_spend_cap_reached-audited");
    expect(within(spend).getByText("Audit only")).toBeInTheDocument();
    expect(spend).toHaveTextContent("A member's spend limit was reached · Ada, Bob and 2 others");
    expect(spend).toHaveTextContent("9 calls would have been refused");
    expect(spend).not.toHaveTextContent("calls refused");

    expect(screen.getByTestId("policy-denial-spend-org_spend_cap_reached-audited")).toHaveTextContent("a hosted app");
    expect(calls).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org-a", days: 7 }));
  });

  it("asks for the window chosen", async () => {
    const calls = setup(() => ({ counts: [], refusedTotal: 0n, auditedTotal: 0n }));
    expect(await screen.findByTestId("policy-denials-empty")).toHaveTextContent("Nothing was refused this week");
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Period" }), "30");
    await waitFor(() => expect(calls).toHaveBeenLastCalledWith(expect.objectContaining({ days: 30 })));
    expect(await screen.findByTestId("policy-denials-empty")).toHaveTextContent("in the last 30 days");
  });

  it("says denial counts are not available on a control plane without the rpc", async () => {
    setup({ error: new ConnectError("unimplemented", Code.Unimplemented) });
    expect(await screen.findByTestId("policy-denials-unavailable")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows a refusal in its own words, never retried", async () => {
    const calls = vi.fn();
    setTransport(
      mockTransport({
        "OrgPolicyService.ListOrgPolicyDenials": () => {
          calls();
          throw new ConnectError("prose", Code.PermissionDenied, { "x-forge-error-reason": "admin_required" });
        },
      }),
    );
    renderWithQuery(<PolicyDenials orgId="org-a" nameOf={nameOf} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Only an admin or owner of this organization can change its policies.");
    expect(calls).toHaveBeenCalledTimes(1);
  });
});

describe("summarizeDenials", () => {
  it("orders policies as the page does, refusals before would-be refusals", () => {
    const rows = summarizeDenials([
      { day: "d", policy: "spend", reason: "r", userId: "u", outcome: "audited", count: 1 },
      { day: "d", policy: "zzz", reason: "r", userId: "u", outcome: "refused", count: 1 },
      { day: "d", policy: "models", reason: "r", userId: "u", outcome: "audited", count: 5 },
      { day: "d", policy: "models", reason: "r", userId: "u", outcome: "refused", count: 1 },
    ]);
    expect(rows.map((r) => `${r.policy}/${r.outcome}`)).toEqual(["models/refused", "models/audited", "spend/audited", "zzz/refused"]);
  });

  it("names a reason it does not know by its own name", () => {
    expect(reasonWords("model_not_allowed")).toBe("A model the policy does not allow");
    expect(reasonWords("something_new")).toBe("Something new");
  });
});
