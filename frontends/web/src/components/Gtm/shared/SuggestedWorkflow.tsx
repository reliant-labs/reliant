// Copyright (c) 2026 Reliant Labs

/**
 * A library entry's or an idea's suggested workflow (suggested_workflow_md):
 * the description, "Create this workflow", and a link to the guide on
 * automating GTM work.
 *
 * GTM never creates a workflow. The workflow builder's New workflow route
 * (/workflow/new) takes no description to prefill (its search is project,
 * drill, tour and chat), so "Create this workflow" copies the description
 * and opens the builder, where a person pastes it. When the copy fails it
 * stays here: the text is selected for a manual copy, with a link to New
 * workflow beside it.
 */

import { useNavigate } from "@tanstack/react-router";
import { ExternalLink, Workflow } from "lucide-react";
import { useRef, useState } from "react";

import Button from "@/components/forge-ui/button";
import Link from "@/components/forge-ui/link";
import { newWorkflowPath } from "@/lib/paths";
import { toast } from "@/lib/toast-manager";

import { OutboundLink } from "./ui";

/** forge's generic guide to automating GTM work. */
export const AUTOMATE_GUIDE_URL = "https://github.com/reliant-labs/forge/blob/main/internal/templates/project/skills/general/gtm/automate/SKILL.md";

export const WORKFLOW_COPIED = "Workflow description copied. Paste it into the new workflow.";
export const WORKFLOW_COPY_FAILED = "Couldn't copy the description. It is selected below: copy it, then open New workflow.";

export function SuggestedWorkflow({ text, title = "Suggested workflow" }: { text: string; title?: string }) {
  const navigate = useNavigate();
  const textRef = useRef<HTMLDivElement>(null);
  const [copyFailed, setCopyFailed] = useState(false);
  if (!text.trim()) return null;

  async function create() {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      // Stay: select the text for a manual copy, and offer the way on.
      setCopyFailed(true);
      const el = textRef.current;
      // Focus first: moving focus can collapse a selection.
      el?.focus();
      const selection = window.getSelection?.();
      if (el && selection) {
        const range = document.createRange();
        range.selectNodeContents(el);
        selection.removeAllRanges();
        selection.addRange(range);
      }
      toast.warning(WORKFLOW_COPY_FAILED);
      return;
    }
    toast.success(WORKFLOW_COPIED);
    void navigate({ to: newWorkflowPath() } as never);
  }

  return (
    <section aria-label={title} className="space-y-2" data-testid="gtm-suggested-workflow">
      <h3 className="text-sm font-medium text-ink">{title}</h3>
      <div
        ref={textRef}
        tabIndex={-1}
        data-testid="gtm-suggested-workflow-text"
        className={`whitespace-pre-wrap rounded-lg border bg-surface-sunken p-3 text-sm text-ink focus:outline-none ${copyFailed ? "border-ink ring-2 ring-ring" : "border-border"}`}
      >
        {text}
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="outline" size="sm" leftIcon={<Workflow />} onClick={() => void create()}>
          Create this workflow
        </Button>
        {copyFailed && (
          <Link href={newWorkflowPath()} className="text-sm font-medium text-ink underline underline-offset-2">
            Open New workflow
          </Link>
        )}
        <OutboundLink url={AUTOMATE_GUIDE_URL} className="inline-flex items-center gap-1 text-xs text-ink-muted underline underline-offset-2 hover:text-ink">
          How to automate this
          <ExternalLink className="size-3" aria-hidden="true" />
        </OutboundLink>
      </div>
    </section>
  );
}
