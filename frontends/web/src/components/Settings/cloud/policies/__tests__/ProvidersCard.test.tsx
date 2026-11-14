/**
 * AI providers card: "Any" is an empty list (today's behaviour); "Only these"
 * starts from everything ticked and keeps at least one; the kinds of account
 * are named by who pays, in the org's own words.
 */
import { create } from "@bufbuild/protobuf";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { OrgPolicyMode, ProviderSource, ProvidersPolicySchema, type ProvidersPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { ProvidersCard } from "../cards/ProvidersCard";
import { renderCard } from "./cardHarness";

describe("ProvidersCard", () => {
  it("tells a member which providers and accounts may serve chats", () => {
    renderCard<ProvidersPolicy>(
      ProvidersCard,
      create(ProvidersPolicySchema, { mode: OrgPolicyMode.ENFORCE, allowed: ["claude", "reliant"], allowedSources: [ProviderSource.ORG_SEATS, ProviderSource.RELIANT] }),
      { editable: false },
    );
    expect(screen.getByTestId("policy-summary-providers")).toHaveTextContent(
      "Chats may run on Claude subscription and Reliant paid through Seats Acme pays for and Reliant credits.",
    );
    expect(screen.getByTestId("policy-providers-sources")).toHaveTextContent("Seats Acme pays for");
    expect(screen.getByText("Enforced")).toBeInTheDocument();
  });

  it("turns on in audit admitting everything, then narrows to chosen providers", async () => {
    const { last } = renderCard<ProvidersPolicy>(ProvidersCard, undefined);
    await userEvent.click(screen.getByRole("switch", { name: "AI providers policy" }));
    expect(last()).toMatchObject({ mode: OrgPolicyMode.AUDIT, allowed: [], allowedSources: [] });

    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Providers" }), "only");
    expect(last()?.allowed).toContain("reliant");
    expect(last()?.allowed).toContain("local");
    await userEvent.click(screen.getByRole("checkbox", { name: "Local models" }));
    expect(last()?.allowed).not.toContain("local");
  });

  it("keeps the last kind of account ticked", async () => {
    const { last } = renderCard<ProvidersPolicy>(
      ProvidersCard,
      create(ProvidersPolicySchema, { mode: OrgPolicyMode.AUDIT, allowedSources: [ProviderSource.ORG_ACCOUNTS, ProviderSource.RELIANT] }),
    );
    await userEvent.click(screen.getByRole("checkbox", { name: "Reliant credits" }));
    expect(last()?.allowedSources).toEqual([ProviderSource.ORG_ACCOUNTS]);
    expect(screen.getByRole("checkbox", { name: "Acme's accounts" })).toBeDisabled();

    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Accounts" }), "any");
    expect(last()?.allowedSources).toEqual([]);
  });
});
