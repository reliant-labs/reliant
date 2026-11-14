/**
 * Integrations card: integrations by their catalog names, governing use in
 * the org's runs (never connecting). An empty list admits none, which is a
 * rule an admin may choose.
 */
import { create } from "@bufbuild/protobuf";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { IntegrationsPolicySchema, OrgPolicyMode, type IntegrationsPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { IntegrationsCard } from "../cards/IntegrationsCard";
import { renderCard } from "./cardHarness";

const CATALOG = {
  "ConnectionService.ListIntegrations": {
    response: { integrations: [{ id: "github", displayName: "GitHub" }, { id: "slack", displayName: "Slack" }] },
  },
};

describe("IntegrationsCard", () => {
  it("names the allowed integrations for a member, and says connecting is still allowed", async () => {
    renderCard<IntegrationsPolicy>(IntegrationsCard, create(IntegrationsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowed: ["github"] }), {
      editable: false,
      handlers: CATALOG,
    });
    await waitFor(() => expect(screen.getByTestId("policy-summary-integrations")).toHaveTextContent("Acme's runs may use only GitHub."));
    expect(screen.getByTestId("policy-summary-integrations")).toHaveTextContent("connecting one is still allowed");
  });

  it("turns on with every catalog integration allowed, and can allow none", async () => {
    const { last, queryClient } = renderCard<IntegrationsPolicy>(IntegrationsCard, undefined, { handlers: CATALOG });
    // The default is built from the catalog: wait for it.
    await waitFor(() => expect(queryClient.getQueryCache().findAll().some((q) => q.state.status === "success")).toBe(true));
    await userEvent.click(screen.getByRole("switch", { name: "Integrations policy" }));
    expect(last()).toMatchObject({ mode: OrgPolicyMode.AUDIT, allowAll: false, allowed: ["github", "slack"] });
    await userEvent.click(screen.getByRole("checkbox", { name: "GitHub" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Slack" }));
    expect(last()?.allowed).toEqual([]);
    expect(screen.getByTestId("policy-summary-integrations")).toHaveTextContent("may use no integration");
  });

  it("allows every integration with an empty list", async () => {
    const { last } = renderCard<IntegrationsPolicy>(IntegrationsCard, create(IntegrationsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowed: ["github"] }), {
      handlers: CATALOG,
    });
    await userEvent.click(screen.getByRole("switch", { name: "Allow every integration" }));
    expect(last()).toMatchObject({ allowAll: true, allowed: [] });
  });
});
