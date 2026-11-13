/**
 * Machine credentials card: what forcing `credentials: none` removes, and an
 * honest statement of what an older machine still exposes.
 */
import { create } from "@bufbuild/protobuf";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { MachinesPolicySchema, OrgPolicyMode, type MachinesPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { MachinesCard } from "../cards/MachinesCard";
import { renderCard } from "./cardHarness";

describe("MachinesCard", () => {
  it("tells a member commands run without credentials, and what an older machine still exposes", () => {
    renderCard<MachinesPolicy>(MachinesCard, create(MachinesPolicySchema, { mode: OrgPolicyMode.ENFORCE, forceCredentialsNone: true }), { editable: false });
    expect(screen.getByTestId("policy-summary-machines")).toHaveTextContent("runs without credentials");
    expect(screen.getByText(/older version gets the environment half only/)).toBeInTheDocument();
    expect(screen.getByText("Enforced")).toBeInTheDocument();
  });

  it("turns on in audit forcing credentials off, and can be relaxed", async () => {
    const { last } = renderCard<MachinesPolicy>(MachinesCard, undefined);
    await userEvent.click(screen.getByRole("switch", { name: "Machine credentials policy" }));
    expect(last()).toMatchObject({ mode: OrgPolicyMode.AUDIT, forceCredentialsNone: true });
    await userEvent.click(screen.getByRole("switch", { name: "Run the organization's commands without credentials" }));
    expect(last()?.forceCredentialsNone).toBe(false);
    expect(screen.getByTestId("policy-summary-machines")).toHaveTextContent("whatever credentials");
  });
});
