/**
 * Spend limits card: limits in words a member can act on, amounts typed in
 * dollars and kept in nanos, an exception per named member, hosted apps
 * counted only when the admin says so, and the member's own usage against
 * the limits (MyPolicyUsage.test covers its states).
 */
import { create } from "@bufbuild/protobuf";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import {
  MemberSpendCapSchema,
  OrgPolicyMode,
  SpendAppliesTo,
  SpendCapSchema,
  SpendPeriod,
  SpendPolicySchema,
  type SpendPolicy,
} from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import type { OrgMember } from "@/services/controlPlane/members";

import { SpendCard } from "../cards/SpendCard";
import { renderCard } from "./cardHarness";

const usd = (n: number) => BigInt(n) * 1_000_000_000n;
const MEMBERS: OrgMember[] = [
  { userId: "u_ada", email: "ada@example.com", name: "Ada", avatarUrl: "", role: "owner", joinedAt: null, isFounder: true, externalId: "" },
  { userId: "u_bob", email: "bob@example.com", name: "", avatarUrl: "", role: "member", joinedAt: null, isFounder: false, externalId: "" },
];

const policy = () =>
  create(SpendPolicySchema, {
    mode: OrgPolicyMode.ENFORCE,
    appliesTo: SpendAppliesTo.MEMBERS,
    orgCap: create(SpendCapSchema, { usdNanos: usd(400), period: SpendPeriod.MONTH }),
    memberDefaultCap: create(SpendCapSchema, { usdNanos: usd(50), period: SpendPeriod.MONTH }),
    memberOverrides: { u_ada: create(MemberSpendCapSchema, { cap: { case: "unlimited", value: true } }) },
  });

describe("SpendCard", () => {
  it("tells a member the limits, who is excepted, and their own usage against them", async () => {
    renderCard<SpendPolicy, { members: OrgMember[] }>(SpendCard, policy(), {
      editable: false,
      extra: { members: MEMBERS },
      handlers: {
        "OrgPolicyService.GetMyPolicyUsage": {
          response: { enforced: true, mode: OrgPolicyMode.ENFORCE, member: { cap: { usdNanos: usd(50), period: SpendPeriod.MONTH }, spentUsdNanos: usd(12) } },
        },
      },
    });
    expect(screen.getByTestId("policy-summary-spend")).toHaveTextContent(
      "the organization spends at most $400 a month; each member at most $50 a month; 1 member has their own limit",
    );
    expect(screen.getByTestId("policy-spend-overrides")).toHaveTextContent("Ada: No limit");
    expect(await screen.findByTestId("policy-usage-member")).toHaveTextContent("You've used $12 of your $50 monthly limit.");
  });

  it("keeps a typed amount in nanos, and ignores what is not one", async () => {
    const { last } = renderCard<SpendPolicy, { members: OrgMember[] }>(SpendCard, policy(), { extra: { members: MEMBERS } });
    const amount = screen.getByRole("textbox", { name: "Limit per member amount" });
    await userEvent.clear(amount);
    await userEvent.type(amount, "75.5");
    expect(last()?.memberDefaultCap?.usdNanos).toBe(75_500_000_000n);
    await userEvent.type(amount, "x");
    expect(amount).toHaveAttribute("aria-invalid", "true");
    expect(last()?.memberDefaultCap?.usdNanos).toBe(75_500_000_000n);

    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Limit per member period" }), "day");
    expect(last()?.memberDefaultCap?.period).toBe(SpendPeriod.DAY);
  });

  it("gives a member an exception, changes it, and removes it", async () => {
    const { last } = renderCard<SpendPolicy, { members: OrgMember[] }>(SpendCard, policy(), { extra: { members: MEMBERS } });
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Member to give an exception" }), "u_bob");
    await userEvent.click(screen.getByRole("button", { name: "Add exception" }));
    expect(last()?.memberOverrides.u_bob?.cap).toEqual({ case: "unlimited", value: true });

    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Limit for bob@example.com" }), "limit");
    expect(last()?.memberOverrides.u_bob?.cap.case).toBe("limit");

    await userEvent.click(screen.getByRole("button", { name: "Remove the exception for bob@example.com" }));
    expect(last()?.memberOverrides).not.toHaveProperty("u_bob");
    expect(last()?.memberOverrides).toHaveProperty("u_ada");
  });

  it("counts hosted apps only when asked, and drops the org limit when switched off", async () => {
    const { last } = renderCard<SpendPolicy, { members: OrgMember[] }>(SpendCard, policy(), { extra: { members: MEMBERS } });
    await userEvent.click(screen.getByRole("switch", { name: "Count hosted apps toward the organization limit" }));
    expect(last()?.appliesTo).toBe(SpendAppliesTo.MEMBERS_AND_APPS);
    await userEvent.click(screen.getByRole("switch", { name: "Limit the organization's total" }));
    expect(last()?.orgCap).toBeUndefined();
  });

  it("turns on in audit with a member limit to start from", async () => {
    const { last } = renderCard<SpendPolicy, { members: OrgMember[] }>(SpendCard, undefined, { extra: { members: MEMBERS } });
    await userEvent.click(screen.getByRole("switch", { name: "Spend limits policy" }));
    expect(last()).toMatchObject({ mode: OrgPolicyMode.AUDIT, appliesTo: SpendAppliesTo.MEMBERS });
    expect(last()?.memberDefaultCap?.period).toBe(SpendPeriod.MONTH);
    expect(last()?.orgCap).toBeUndefined();
  });
});
