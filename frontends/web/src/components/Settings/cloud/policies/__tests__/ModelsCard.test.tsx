/**
 * Models card: a member reads which models are allowed and the rule's mode;
 * an admin turns it on in audit with every gateway model ticked, can never
 * untick the last one (an empty list would stop every chat), and can switch
 * to "every model" or to Enforce.
 */
import { create } from "@bufbuild/protobuf";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { ModelsPolicySchema, OrgPolicyMode, type ModelsPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { ModelsCard } from "../cards/ModelsCard";
import { renderCard } from "./cardHarness";

const MODELS = { "LLMGatewayService.ListAvailableModels": { response: { models: ["gpt-5", "claude-sonnet-4"] } } };

describe("ModelsCard", () => {
  it("shows a member the allowed models and the audit badge, with nothing to edit", () => {
    renderCard<ModelsPolicy>(ModelsCard, create(ModelsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowed: ["claude-sonnet-4"] }), { editable: false });
    const card = screen.getByTestId("policy-card-models");
    expect(within(card).getByText("Audit only")).toBeInTheDocument();
    expect(screen.getByTestId("policy-summary-models")).toHaveTextContent("Chats may use only claude-sonnet-4");
    expect(screen.getByTestId("policy-models-allowed")).toHaveTextContent("claude-sonnet-4");
    expect(within(card).queryByRole("switch")).toBeNull();
    expect(within(card).queryByRole("combobox")).toBeNull();
  });

  it("turns on in audit with every gateway model allowed", async () => {
    const { last, queryClient } = renderCard<ModelsPolicy>(ModelsCard, undefined, { handlers: MODELS });
    expect(screen.getByTestId("policy-summary-models")).toHaveTextContent("Any model");
    // An editor's card reads the models up front: the rule starts from all of them.
    await waitFor(() => expect(queryClient.getQueryCache().findAll().some((q) => q.state.status === "success")).toBe(true));
    await userEvent.click(screen.getByRole("switch", { name: "Models policy" }));
    expect(last()?.mode).toBe(OrgPolicyMode.AUDIT);
    expect(last()?.allowed).toEqual(["claude-sonnet-4", "gpt-5"]);
    expect(await screen.findByRole("checkbox", { name: "gpt-5" })).toBeChecked();
  });

  it("never lets the last allowed model be unticked", async () => {
    const { last } = renderCard<ModelsPolicy>(ModelsCard, create(ModelsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowed: ["claude-sonnet-4", "gpt-5"] }), { handlers: MODELS });
    await userEvent.click(await screen.findByRole("checkbox", { name: "gpt-5" }));
    expect(last()?.allowed).toEqual(["claude-sonnet-4"]);
    expect(screen.getByRole("checkbox", { name: "claude-sonnet-4" })).toBeDisabled();
  });

  it("allows every model with an empty list, and enforces when asked", async () => {
    const { last } = renderCard<ModelsPolicy>(ModelsCard, create(ModelsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowed: ["gpt-5"] }), { handlers: MODELS });
    await userEvent.click(screen.getByRole("switch", { name: "Allow every model" }));
    expect(last()).toMatchObject({ allowAll: true, allowed: [] });
    expect(screen.getByTestId("policy-summary-models")).toHaveTextContent("Every model");

    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Models mode" }), "enforce");
    expect(last()?.mode).toBe(OrgPolicyMode.ENFORCE);
    expect(within(screen.getByTestId("policy-card-models")).getByText("Enforced")).toBeInTheDocument();
    expect(screen.queryByText(/Audit mode: nothing is blocked/)).toBeNull();
  });

  it("removes the rule when switched off", async () => {
    const { last } = renderCard<ModelsPolicy>(ModelsCard, create(ModelsPolicySchema, { mode: OrgPolicyMode.AUDIT, allowAll: true }), { handlers: MODELS });
    await userEvent.click(screen.getByRole("switch", { name: "Models policy" }));
    expect(last()).toBeUndefined();
    expect(screen.getByText("Off")).toBeInTheDocument();
  });
});
