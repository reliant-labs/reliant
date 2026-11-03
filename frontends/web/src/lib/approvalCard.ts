// Copyright (c) 2025 Reliant Labs

/**
 * Reads an approval's title as the card it is.
 *
 * An approval carries one string, its title. Most are one sentence ("Run
 * deploy?"). An agent's request to turn on or change an automation is a card
 * the server lays out for people (internal/handlers/trigger/activation_card.go):
 *
 *   <one plain sentence: what turning it on does>
 *   <Label>: <value>
 *   ...
 *
 *   Technical details
 *   <technical lines>
 *
 * The server flattens every value to one line, so a value cannot forge a row
 * or the heading. Keep DETAILS_HEADING in step with `cardDetailsHeading`.
 */

export const DETAILS_HEADING = "Technical details";

export interface ApprovalCardRow {
  label: string;
  value: string;
}

export interface ApprovalCard {
  /** The first line: what is being approved, in one sentence. */
  lead: string;
  /** Labelled rows under the lead. */
  rows: ApprovalCardRow[];
  /** Lines under the lead that are not "Label: value" rows. */
  notes: string[];
  /** The technical lines, for a disclosure. Empty when there are none. */
  details: string[];
  /**
   * The approval grants an automation standing power (an activation card).
   * It may be approved in an "Approve all" only once the batch confirmation
   * has shown what it grants (planApprovalBatch).
   */
  grantsPower: boolean;
  /**
   * An activation approval in the plain-text form servers wrote before the
   * card: it has no plain-words account of what it grants, so it is never in
   * an "Approve all" and is decided on its own.
   */
  legacyActivation: boolean;
}

const ROW = /^([A-Z][A-Za-z ]{1,40}): (.+)$/;

/**
 * An activation operation's technical line. Every activation approval carries
 * one, in this card's details and in the plain-text title servers wrote
 * before it ("Approval: …\nMachine: …\nOperation: create_trigger_activation"),
 * which pending approvals and an older server still return. Reading either
 * as standing power is the conservative direction: a line that wrongly
 * matches only asks for more reading before an approval is decided.
 */
const ACTIVATION_OPERATION = /^Operation: (create_trigger_activation|update_trigger_activation|enable_trigger)\b/m;

export function parseApprovalCard(title: string): ApprovalCard {
  const separator = `\n\n${DETAILS_HEADING}\n`;
  const at = title.indexOf(separator);
  const body = at >= 0 ? title.slice(0, at) : title;
  const details = at >= 0 ? splitLines(title.slice(at + separator.length)) : [];
  const [lead = "", ...rest] = splitLines(body);
  const rows: ApprovalCardRow[] = [];
  const notes: string[] = [];
  for (const line of rest) {
    const match = at >= 0 ? ROW.exec(line) : null;
    if (match) rows.push({ label: match[1], value: match[2] });
    else notes.push(line);
  }
  const activation = ACTIVATION_OPERATION.test(title);
  // A card says what it grants in its lead and rows; without both it is read
  // as the plain-text form, however it is laid out.
  const summarised = at >= 0 && lead !== "" && rows.length > 0;
  return {
    lead,
    rows,
    notes,
    details,
    grantsPower: at >= 0 || activation,
    legacyActivation: activation && !summarised,
  };
}

function splitLines(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

/** Why an approval is left out of "Approve all", shown beside it. */
export const LEGACY_ACTIVATION_REASON =
  "Not in Approve all: this request was raised before approval cards said in plain words what they grant. Read its details and decide it on its own.";

/** How "Approve all" treats a set of pending approvals. */
export interface ApprovalBatchPlan<T> {
  /** Decided by one Approve all / Deny all: routine approvals and activation cards that say what they grant. */
  together: T[];
  /** The activation cards in `together`: the confirmation shows each one's grant summary before anything is approved. */
  grants: T[];
  /** Decided one by one, each with why: they cannot show a grant summary. */
  apart: { approval: T; reason: string }[];
}

/**
 * Splits pending approvals for "Approve all". An activation card may be
 * approved with the others, after the confirmation shows what it grants
 * (owner decision on reliant #1988, 2026-11-02). An activation approval in
 * the old plain-text form has nothing to show there, so it stays out, with
 * the reason beside it: approving it in bulk would grant standing power
 * nobody was shown in plain words.
 */
export function planApprovalBatch<T extends { title: string }>(approvals: readonly T[]): ApprovalBatchPlan<T> {
  const plan: ApprovalBatchPlan<T> = { together: [], grants: [], apart: [] };
  for (const approval of approvals) {
    const card = parseApprovalCard(approval.title);
    if (card.legacyActivation) {
      plan.apart.push({ approval, reason: LEGACY_ACTIVATION_REASON });
      continue;
    }
    plan.together.push(approval);
    if (card.grantsPower) plan.grants.push(approval);
  }
  return plan;
}
