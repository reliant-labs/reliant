import { describe, expect, it } from "vitest";
import { approvalGrantsPower, DETAILS_HEADING, parseApprovalCard } from "../approvalCard";

const activation = [
  "Turn on PR review for acme/shopfront: it will run on each GitHub event it watches (pull_request.opened).",
  "Approval needed: A new activation is new standing power.",
  "Repositories: acme/shopfront",
  "Machine: None: it runs on Reliant's servers",
  "",
  DETAILS_HEADING,
  "Operation: create_trigger_activation",
  'Activation: {"name":"pr"}',
].join("\n");

describe("parseApprovalCard", () => {
  it("reads an activation card as lead, rows and technical details", () => {
    const card = parseApprovalCard(activation);
    expect(card.lead).toMatch(/^Turn on PR review/);
    expect(card.rows).toEqual([
      { label: "Approval needed", value: "A new activation is new standing power." },
      { label: "Repositories", value: "acme/shopfront" },
      { label: "Machine", value: "None: it runs on Reliant's servers" },
    ]);
    expect(card.details).toEqual(["Operation: create_trigger_activation", 'Activation: {"name":"pr"}']);
    expect(card.grantsPower).toBe(true);
  });

  it("reads an activation approval in the plain-text form older servers wrote as standing power", () => {
    // Pending approvals stored before the card, and an older server during a
    // rollout, carry this title. It must never join an "Approve all".
    for (const operation of ["create_trigger_activation", "update_trigger_activation", "enable_trigger"]) {
      const legacy = [
        "Approval: A new activation is new standing power.",
        "Machine: laptop (id d_1)",
        `Operation: ${operation}`,
        "Run as user: u_1; integration identity: owner",
        'Activation: {"name":"pr"}',
      ].join("\n");
      expect(approvalGrantsPower(legacy), operation).toBe(true);
    }
  });

  it("reads a one-sentence approval as its lead alone", () => {
    const card = parseApprovalCard("Deploy to production?");
    expect(card).toEqual({ lead: "Deploy to production?", rows: [], notes: [], details: [], grantsPower: false });
  });

  it("keeps the extra lines of a plain multi-line approval as notes, not rows", () => {
    const card = parseApprovalCard("Run the migration?\nTarget: prod");
    expect(card.rows).toEqual([]);
    expect(card.notes).toEqual(["Target: prod"]);
    expect(approvalGrantsPower("Run the migration?\nTarget: prod")).toBe(false);
  });
});
