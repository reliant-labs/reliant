import { describe, expect, it } from "vitest";
import { DETAILS_HEADING, LEGACY_ACTIVATION_REASON, parseApprovalCard, planApprovalBatch } from "../approvalCard";

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
    expect(card.legacyActivation).toBe(false);
  });

  it("reads an activation approval in the plain-text form older servers wrote as standing power", () => {
    // Pending approvals stored before the card, and an older server during a
    // rollout, carry this title. It says nothing in plain words about what it
    // grants, so it must never join an "Approve all".
    for (const operation of ["create_trigger_activation", "update_trigger_activation", "enable_trigger"]) {
      const legacy = [
        "Approval: A new activation is new standing power.",
        "Machine: laptop (id d_1)",
        `Operation: ${operation}`,
        "Run as user: u_1; integration identity: owner",
        'Activation: {"name":"pr"}',
      ].join("\n");
      const card = parseApprovalCard(legacy);
      expect(card.grantsPower, operation).toBe(true);
      expect(card.legacyActivation, operation).toBe(true);
    }
  });

  it("reads a one-sentence approval as its lead alone", () => {
    const card = parseApprovalCard("Deploy to production?");
    expect(card).toEqual({
      lead: "Deploy to production?",
      rows: [],
      notes: [],
      details: [],
      grantsPower: false,
      legacyActivation: false,
    });
  });

  it("keeps the extra lines of a plain multi-line approval as notes, not rows", () => {
    const card = parseApprovalCard("Run the migration?\nTarget: prod");
    expect(card.rows).toEqual([]);
    expect(card.notes).toEqual(["Target: prod"]);
    expect(parseApprovalCard("Run the migration?\nTarget: prod").grantsPower).toBe(false);
  });
});

describe("planApprovalBatch", () => {
  const legacy = ["Approval: A new activation is new standing power.", "Operation: create_trigger_activation"].join("\n");
  // An interim form (#1988's first line, then the plain-text rows): still no summary.
  const interim = ["create_trigger_activation triage · nightly", "Approval: A new activation", "Operation: create_trigger_activation"].join("\n");

  it("puts routine approvals and summarised activation cards together, and lists the cards to show", () => {
    const plan = planApprovalBatch([
      { id: "r", title: "Deploy to production?" },
      { id: "a", title: activation },
    ]);
    expect(plan.together.map((a) => a.id)).toEqual(["r", "a"]);
    expect(plan.grants.map((a) => a.id)).toEqual(["a"]);
    expect(plan.apart).toEqual([]);
  });

  it("keeps activation approvals with no plain-words summary apart, with the reason", () => {
    const plan = planApprovalBatch([
      { id: "r", title: "Deploy to production?" },
      { id: "l", title: legacy },
      { id: "i", title: interim },
    ]);
    expect(plan.together.map((a) => a.id)).toEqual(["r"]);
    expect(plan.grants).toEqual([]);
    expect(plan.apart.map(({ approval, reason }) => [approval.id, reason])).toEqual([
      ["l", LEGACY_ACTIVATION_REASON],
      ["i", LEGACY_ACTIVATION_REASON],
    ]);
  });

  it("reads a card with the details heading but no labelled rows as unsummarised", () => {
    const bare = ["Turn on something.", "", DETAILS_HEADING, "Operation: create_trigger_activation"].join("\n");
    expect(parseApprovalCard(bare).legacyActivation).toBe(true);
    expect(planApprovalBatch([{ id: "b", title: bare }]).apart).toHaveLength(1);
  });
});
