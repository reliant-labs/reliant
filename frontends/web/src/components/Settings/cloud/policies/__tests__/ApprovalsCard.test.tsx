/**
 * Approvals card: floors in words, raising strictness only; plan mode is
 * never touched.
 */
import { create } from "@bufbuild/protobuf";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import {
  ApprovalsPolicySchema,
  AutomationApprovalFloor,
  ChatModeFloor,
  OrgPolicyMode,
  type ApprovalsPolicy,
} from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { ApprovalsCard } from "../cards/ApprovalsCard";
import { renderCard } from "./cardHarness";

describe("ApprovalsCard", () => {
  it("says what the floor requires", () => {
    renderCard<ApprovalsPolicy>(
      ApprovalsCard,
      create(ApprovalsPolicySchema, { mode: OrgPolicyMode.AUDIT, automationFloor: AutomationApprovalFloor.ALWAYS, chatModeFloor: ChatModeFloor.NO_AUTO }),
      { editable: false },
    );
    const summary = screen.getByTestId("policy-summary-approvals");
    expect(summary).toHaveTextContent("Every agent change to an automation needs its owner's approval");
    expect(summary).toHaveTextContent("chats can't run in auto mode");
    expect(summary).toHaveTextContent("plan mode is unaffected");
  });

  it("turns on with the capability-change floor, and sets each floor", async () => {
    const { last } = renderCard<ApprovalsPolicy>(ApprovalsCard, undefined);
    await userEvent.click(screen.getByRole("switch", { name: "Approvals policy" }));
    expect(last()).toMatchObject({ mode: OrgPolicyMode.AUDIT, automationFloor: AutomationApprovalFloor.ON_CAPABILITY_CHANGE, chatModeFloor: ChatModeFloor.ANY });

    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Automation changes need approval" }), String(AutomationApprovalFloor.ALWAYS));
    expect(last()?.automationFloor).toBe(AutomationApprovalFloor.ALWAYS);
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Chat modes" }), String(ChatModeFloor.NO_AUTO));
    expect(last()?.chatModeFloor).toBe(ChatModeFloor.NO_AUTO);
  });

  it("says a floor of nothing changes nothing", () => {
    renderCard<ApprovalsPolicy>(ApprovalsCard, create(ApprovalsPolicySchema, { mode: OrgPolicyMode.AUDIT }), { editable: false });
    expect(screen.getByTestId("policy-summary-approvals")).toHaveTextContent("changes nothing");
  });
});
