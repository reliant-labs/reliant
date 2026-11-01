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
   * Such an approval is decided on its own, never in an "Approve all".
   */
  grantsPower: boolean;
}

const ROW = /^([A-Z][A-Za-z ]{1,40}): (.+)$/;

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
  return { lead, rows, notes, details, grantsPower: at >= 0 };
}

function splitLines(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

/** True when the approval grants standing power and must be decided alone. */
export function approvalGrantsPower(title: string): boolean {
  return parseApprovalCard(title).grantsPower;
}
