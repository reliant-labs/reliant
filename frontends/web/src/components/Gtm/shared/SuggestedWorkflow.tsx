// Copyright (c) 2026 Reliant Labs

/**
 * A library entry's or an idea's suggested workflow (suggested_workflow_md):
 * the description, "Create this workflow", and a link to the guide on
 * automating GTM work.
 *
 * GTM never creates a workflow. The workflow builder's New workflow route
 * (/workflow/new) takes no description to prefill (its search is project,
 * drill, tour and chat), so "Create this workflow" copies the description
 * and opens the builder, where a person pastes it.
 */

import { useNavigate } from "@tanstack/react-router";
import { ExternalLink, Workflow } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { newWorkflowPath } from "@/lib/paths";
import { toast } from "@/lib/toast-manager";

import { OutboundLink } from "./ui";

/** forge's generic guide to automating GTM work. */
export const AUTOMATE_GUIDE_URL = "https://github.com/reliant-labs/forge/blob/main/internal/templates/project/skills/general/gtm/automate/SKILL.md";

export const WORKFLOW_COPIED = "Workflow description copied. Paste it into the new workflow.";

export function SuggestedWorkflow({ text, title = "Suggested workflow" }: { text: string; title?: string }) {
  const navigate = useNavigate();
  if (!text.trim()) return null;

  async function create() {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(WORKFLOW_COPIED);
    } catch {
      toast.warning("Couldn't copy the description. Copy it from here, then paste it into the new workflow.");
    }
    void navigate({ to: newWorkflowPath() } as never);
  }

  return (
    <section aria-label={title} className="space-y-2" data-testid="gtm-suggested-workflow">
      <h3 className="text-sm font-medium text-ink">{title}</h3>
      <div className="whitespace-pre-wrap rounded-lg border border-border bg-surface-sunken p-3 text-sm text-ink">{text}</div>
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="outline" size="sm" leftIcon={<Workflow />} onClick={() => void create()}>
          Create this workflow
        </Button>
        <OutboundLink url={AUTOMATE_GUIDE_URL} className="inline-flex items-center gap-1 text-xs text-ink-muted underline underline-offset-2 hover:text-ink">
          How to automate this
          <ExternalLink className="size-3" aria-hidden="true" />
        </OutboundLink>
      </div>
    </section>
  );
}
