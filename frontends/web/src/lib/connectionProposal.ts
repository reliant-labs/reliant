// Copyright (c) 2025 Reliant Labs

/**
 * The small, dependency-free half of an agent's connection proposal
 * (propose_connection; components/Chat/ConnectionProposalCard.tsx is the
 * card). The OAuth callback page reads the return-path parameter without
 * loading the card.
 */

import type { Approval } from "../hooks/approval-queries";

/** A pending approval that is an agent's connection proposal. */
export type ProposalApproval = Approval & { connectionProposal: NonNullable<Approval["connectionProposal"]> };

/** The approval as a connection proposal, or undefined when it is not one. */
export function asConnectionProposal<A extends Approval>(approval: A): (A & ProposalApproval) | undefined {
  return approval.connectionProposal ? (approval as A & ProposalApproval) : undefined;
}

/** The action_taken of a card denied because its dialog was closed unconnected. */
export const CONNECTION_NOT_COMPLETED = "not_completed";

/** The return-path parameter naming the card a web sign-in approves. */
export const CONNECTION_PROPOSAL_PARAM = "connection_proposal";

/** The current page with `connection_proposal=<approvalId>`: where a web sign-in lands. */
export function proposalReturnPath(approvalId: string): string {
  if (typeof window === "undefined") return "/";
  const url = new URL(window.location.href);
  url.searchParams.set(CONNECTION_PROPOSAL_PARAM, approvalId);
  return `${url.pathname}${url.search}`;
}

/** The Deny request answering a proposal: declined, or its dialog closed unconnected. */
export function proposalDenial(
  requestId: string,
  notCompleted: boolean,
): { requestId: string; denialReason: string; actionTaken?: string } {
  return notCompleted
    ? { requestId, denialReason: "The connection dialog was closed without connecting", actionTaken: CONNECTION_NOT_COMPLETED }
    : { requestId, denialReason: "Declined" };
}
