// Copyright (c) 2025 Reliant Labs

/**
 * "…or create a Reliant workflow to automate this": the last line of the
 * handoff sheet (AskAgentSheet), where there is no Reliant chat to start. A
 * small "What is Reliant?" disclosure explains it in two lines and links to
 * the product page, tagged so the demo's visits are counted.
 */

import { useState } from "react";
import { ExternalLink } from "lucide-react";

import { trackEvent } from "@/lib/analytics";

/** Reliant's product page, tagged as coming from the forge demo. */
export const RELIANT_PRODUCT_URL = "https://reliantlabs.io/products/reliant?utm_source=demo&utm_campaign=forge_demo";

export function ReliantWorkflowNote() {
  const [open, setOpen] = useState(false);
  return (
    <div className="space-y-2 text-xs text-ink-muted" data-testid="ask-agent-reliant-workflow">
      <p>
        …or create a Reliant workflow to automate this: every new issue investigated by an agent, without you pasting anything.{" "}
        <button
          type="button"
          aria-expanded={open}
          onClick={() => {
            setOpen((prev) => !prev);
            if (!open) trackEvent("demo_what_is_reliant_opened", {});
          }}
          className="font-medium text-ink underline underline-offset-2"
          data-testid="what-is-reliant-toggle"
        >
          What is Reliant?
        </button>
      </p>
      {open && (
        <div className="rounded-md border border-border/60 bg-surface p-3" data-testid="what-is-reliant">
          <p className="text-ink">
            Reliant runs coding agents for you, on a schedule or when something happens, such as an alert or a new issue. A workflow says
            which agents run, in what order, and where a person approves.
          </p>
          <a
            href={RELIANT_PRODUCT_URL}
            target="_blank"
            rel="noopener noreferrer"
            onClick={() => trackEvent("demo_cta_clicked", { target: "reliant", placement: "ask_agent" })}
            className="mt-2 inline-flex items-center gap-1 font-medium text-primary-ink hover:underline"
            data-testid="what-is-reliant-link"
          >
            Learn about Reliant
            <ExternalLink className="h-3 w-3" aria-hidden="true" />
          </a>
        </div>
      )}
    </div>
  );
}
