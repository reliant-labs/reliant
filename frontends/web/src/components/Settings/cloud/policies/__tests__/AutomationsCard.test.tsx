/**
 * Automations card: unattended runs may use only what chats may. It offers
 * only the sources the AI providers rule admits, starts from the ones the org
 * pays for, and flags a stored source chats may no longer use.
 */
import { create } from "@bufbuild/protobuf";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import {
  AutomationsPolicySchema,
  OrgPolicyMode,
  ProviderSource,
  ProvidersPolicySchema,
  type AutomationsPolicy,
} from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { AutomationsCard } from "../cards/AutomationsCard";
import { renderCard } from "./cardHarness";

const providers = (sources: ProviderSource[]) => create(ProvidersPolicySchema, { mode: OrgPolicyMode.AUDIT, allowedSources: sources });

describe("AutomationsCard", () => {
  it("says what automations may use, in the org's words", () => {
    renderCard(AutomationsCard, create(AutomationsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowedSources: [ProviderSource.ORG_ACCOUNTS, ProviderSource.RELIANT] }), {
      editable: false,
      extra: { providers: undefined },
    });
    expect(screen.getByTestId("policy-summary-automations")).toHaveTextContent("Automations may use: Acme's accounts and Reliant credits.");
  });

  it("starts from the sources the org pays for, within what chats may use", async () => {
    const { last } = renderCard<AutomationsPolicy, { providers: ReturnType<typeof providers> }>(AutomationsCard, undefined, {
      extra: { providers: providers([ProviderSource.RELIANT, ProviderSource.PERSONAL]) },
    });
    await userEvent.click(screen.getByRole("switch", { name: "Automations policy" }));
    expect(last()).toMatchObject({ mode: OrgPolicyMode.AUDIT, allowedSources: [ProviderSource.RELIANT] });
    // Only what chats may use is offered.
    expect(screen.getByRole("checkbox", { name: "Members' own accounts" })).not.toBeChecked();
    expect(screen.queryByRole("checkbox", { name: "Acme's accounts" })).toBeNull();
  });

  it("flags a stored source chats may no longer use, so it can be removed", async () => {
    const { last } = renderCard(
      AutomationsCard,
      create(AutomationsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowedSources: [ProviderSource.RELIANT, ProviderSource.PERSONAL] }),
      { extra: { providers: providers([ProviderSource.RELIANT]) } },
    );
    expect(screen.getByText(/Not allowed for chats/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: /Members' own accounts/ }));
    expect(last()?.allowedSources).toEqual([ProviderSource.RELIANT]);
  });
});
