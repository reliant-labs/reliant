// Copyright (c) 2025 Reliant Labs

/**
 * What an approval asks, laid out to be read before it is answered: the lead
 * sentence, its labelled rows, and the technical lines behind a disclosure.
 * Used by the chat's approval panel and the Inbox, so both show the same
 * card. A one-sentence approval ("Deploy to production?") is just its lead.
 */

import { ChevronRight } from "lucide-react";
import { parseApprovalCard } from "@/lib/approvalCard";
import { cn } from "@/lib/utils";

interface ApprovalCardViewProps {
  title: string;
  /** Hide the lead, when the surrounding row already shows it. */
  hideLead?: boolean;
  className?: string;
}

export function ApprovalCardView({ title, hideLead, className }: ApprovalCardViewProps) {
  const card = parseApprovalCard(title);
  return (
    <div className={cn("min-w-0 space-y-2 text-ui text-foreground", className)}>
      {!hideLead && card.lead && <p className="break-words font-medium">{card.lead}</p>}
      {card.rows.length > 0 && (
        <dl className="grid grid-cols-1 gap-x-4 gap-y-1.5 sm:grid-cols-[minmax(8rem,auto)_1fr]">
          {card.rows.map((row) => (
            <div key={row.label} className="contents">
              <dt className="text-meta text-muted-foreground sm:pt-px">{row.label}</dt>
              <dd className="min-w-0 break-words">{row.value}</dd>
            </div>
          ))}
        </dl>
      )}
      {card.notes.length > 0 && (
        <div className="space-y-1">
          {card.notes.map((note, index) => (
            <p key={index} className="break-words">
              {note}
            </p>
          ))}
        </div>
      )}
      {card.details.length > 0 && (
        <details className="group">
          <summary className="flex cursor-pointer list-none items-center gap-1.5 rounded-md py-1 text-ui text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
            <ChevronRight className="size-3.5 transition-transform group-open:rotate-90" aria-hidden="true" />
            Technical details
          </summary>
          <ul className="ml-2 mt-1 space-y-1 border-l border-border-subtle pl-4 font-mono text-code text-muted-foreground">
            {card.details.map((line, index) => (
              <li key={index} className="break-words">
                {line}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
