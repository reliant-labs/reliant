// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Positioning & facts over GtmService (a mock transport): the
 * positioning and its audiences, an edit on the revision on screen (0 when
 * there is none yet), and Verify, a person's act that records the source it
 * was checked against.
 */

import { create } from "@bufbuild/protobuf";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  GtmBusinessModel,
  GtmBusinessType,
  GtmClaimCategory,
  GtmClaimSchema,
  GtmClaimSourceKind,
  GtmClaimStatus,
  GtmPositioningSchema,
  type UpdatePositioningRequest,
} from "@/gen/reliant/v1/gtm_pb";

import { renderGtm, TEST_ORG } from "../../shared/__tests__/gtmHarness";
import { PositioningView } from "../PositioningPage";

vi.mock("@/lib/toast-manager", () => ({ toast: { warning: vi.fn(), error: vi.fn(), info: vi.fn() } }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const positioning = create(GtmPositioningSchema, { orgId: TEST_ORG, positioningMd: "Hosting for people who ship.\n\nOne command.", audiences: ["Solo founders", "Agencies"], updatedBy: "Ada", revision: 3, businessType: GtmBusinessType.DEVELOPER_TOOLS, businessModels: [GtmBusinessModel.SUBSCRIPTION] });
const unverified = create(GtmClaimSchema, { id: "c-1", orgId: TEST_ORG, text: "One static site per org is free", category: GtmClaimCategory.PRICING, status: GtmClaimStatus.UNVERIFIED, revision: 2 });
const verified = create(GtmClaimSchema, { id: "c-2", orgId: TEST_ORG, text: "Deploys finish in a minute", status: GtmClaimStatus.VERIFIED, verifiedBy: "Ada", sourceUrl: "https://docs.example/deploy", sourceKind: GtmClaimSourceKind.DOCS, revision: 1 });
const comparison = create(GtmClaimSchema, { id: "c-3", orgId: TEST_ORG, text: "Faster than the other one", category: GtmClaimCategory.COMPARISON, status: GtmClaimStatus.UNVERIFIED, revision: 1 });

const reads = {
  "GtmService.GetPositioning": { response: { positioning } },
  "GtmService.ListClaims": { response: { claims: [unverified, verified, comparison] } },
};

describe("PositioningView", () => {
  it("shows the positioning, its audiences and the facts", async () => {
    renderGtm(<PositioningView />, reads, { path: "/gtm/positioning" });
    expect(await screen.findByText("Solo founders")).toBeInTheDocument();
    expect(screen.getByText("Hosting for people who ship.")).toBeInTheDocument();
    expect(await screen.findByText("1 of 3 verified by a person. Claim checks count only verified facts.")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /^Verify:/ })).toHaveLength(2);
    // A fact's source opens in a new tab, without an opener.
    const source = screen.getByRole("link", { name: "Docs" });
    expect(source).toHaveAttribute("href", "https://docs.example/deploy");
    expect(source).toHaveAttribute("target", "_blank");
    expect(source).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("saves an edit on the revision on screen", async () => {
    const update = vi.fn(() => ({ positioning }));
    renderGtm(<PositioningView />, { ...reads, "GtmService.UpdatePositioning": update }, { path: "/gtm/positioning" });
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText("Positioning"), { target: { value: "New positioning." } });
    fireEvent.change(screen.getByLabelText(/Audiences/), { target: { value: "Solo founders,  Teams ," } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, expectedRevision: 3, positioningMd: "New positioning.", audiences: ["Solo founders", "Teams"] }));
    // The business profile goes with every save, unchanged when untouched.
    expect(update).toHaveBeenCalledWith(expect.objectContaining({ businessType: GtmBusinessType.DEVELOPER_TOOLS, businessModels: [GtmBusinessModel.SUBSCRIPTION] }));
  });

  it("shows the business profile, and saves a type and several models", async () => {
    const update = vi.fn(() => ({ positioning }));
    renderGtm(<PositioningView />, { ...reads, "GtmService.UpdatePositioning": update }, { path: "/gtm/positioning" });
    const profile = await screen.findByTestId("gtm-business-profile");
    expect(within(profile).getByText("Developer tools")).toBeInTheDocument();
    expect(within(profile).getByText("Subscription")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText("Business type"), { target: { value: String(GtmBusinessType.B2B_SAAS) } });
    const models = screen.getByRole("group", { name: "Business models" });
    fireEvent.click(within(models).getByLabelText("Usage-based"));
    fireEvent.click(within(models).getByLabelText("Freemium"));
    fireEvent.click(within(models).getByLabelText("Subscription"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    const req = (update.mock.calls[0] as unknown[])[0] as UpdatePositioningRequest;
    expect(req.businessType).toBe(GtmBusinessType.B2B_SAAS);
    expect(req.businessModels).toEqual([GtmBusinessModel.USAGE_BASED, GtmBusinessModel.FREEMIUM]);
  });

  it("creates the positioning at revision 0 when there is none (an empty one at revision 0)", async () => {
    const update = vi.fn(() => ({ positioning }));
    renderGtm(
      <PositioningView />,
      { ...reads, "GtmService.GetPositioning": { response: { positioning: { orgId: TEST_ORG, revision: 0 } } }, "GtmService.UpdatePositioning": update },
      { path: "/gtm/positioning" },
    );
    expect(await screen.findByText(/No positioning yet/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText("Positioning"), { target: { value: "First." } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({ expectedRevision: 0, positioningMd: "First." })));
  });

  it("verifies a fact as a person, recording the source it was checked against", async () => {
    const upsert = vi.fn(() => ({ claim: { ...unverified, status: GtmClaimStatus.VERIFIED } }));
    renderGtm(<PositioningView />, { ...reads, "GtmService.UpsertClaim": upsert }, { path: "/gtm/positioning" });
    fireEvent.click(await screen.findByRole("button", { name: "Verify: One static site per org is free" }));
    const dialog = await screen.findByTestId("person-action-dialog-verify");
    expect(within(dialog).getByText("Only a person")).toBeInTheDocument();
    const confirm = screen.getByRole("button", { name: "Mark verified" });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Source you checked it against"), { target: { value: "https://docs.example/pricing" } });
    fireEvent.click(confirm);
    await waitFor(() => expect(upsert).toHaveBeenCalled());
    expect(upsert).toHaveBeenCalledWith(
      expect.objectContaining({ orgId: TEST_ORG, id: "c-1", expectedRevision: 2, status: GtmClaimStatus.VERIFIED, sourceUrl: "https://docs.example/pricing", sourceKind: GtmClaimSourceKind.DOCS }),
    );
  });

  it("verifies a comparison only against a primary third-party source", async () => {
    const upsert = vi.fn(() => ({ claim: comparison }));
    renderGtm(<PositioningView />, { ...reads, "GtmService.UpsertClaim": upsert }, { path: "/gtm/positioning" });
    fireEvent.click(await screen.findByRole("button", { name: "Verify: Faster than the other one" }));
    fireEvent.change(await screen.findByLabelText("Source you checked it against"), { target: { value: "https://them.example/benchmarks" } });
    fireEvent.click(screen.getByRole("button", { name: "Mark verified" }));
    await waitFor(() => expect(upsert).toHaveBeenCalledWith(expect.objectContaining({ sourceKind: GtmClaimSourceKind.PRIMARY_THIRD_PARTY })));
  });
});
