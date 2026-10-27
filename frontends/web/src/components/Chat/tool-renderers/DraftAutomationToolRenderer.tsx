// Copyright (c) 2025 Reliant Labs

/**
 * draft_automation (ui-review report 03, §6.7 option B): the agent drafts an
 * automation — when it runs and what each run does — and the person reviews
 * it. The card shows the draft in words with one action, "Review & create",
 * which opens the automation dialog prefilled. Nothing is created until the
 * person presses Create there; the agent has no tool that turns anything on.
 *
 * Rendered inline in the transcript (ChatMessage), like request_machine: it is
 * a decision for the person, not a record of work.
 */

import { memo } from "react";
import { CalendarClock } from "lucide-react";

import { automationDraftFromJson, type AutomationDraft } from "@/hooks/trigger-model";
import { useChat } from "@/hooks/chat-queries";
import { draftToPrefill } from "../../Automations/dialog/draftToPrefill";
import { openAutomationDialog } from "../../Automations/dialog/openAutomationDialog";
import Button from "../../forge-ui/button";
import type { ToolContentProps } from "./types";

/** The draft in a draft_automation result's metadata, or undefined (an error, or still running). */
export function draftFromToolResult(metadata: string | undefined): AutomationDraft | undefined {
  if (!metadata) return undefined;
  try {
    const parsed: unknown = JSON.parse(metadata);
    return parsed && typeof parsed === "object" ? automationDraftFromJson((parsed as { draft?: unknown }).draft) : undefined;
  } catch {
    return undefined;
  }
}

export function isDraftAutomationTool(name: string): boolean {
  return name.toLowerCase() === "draft_automation" || name.toLowerCase().endsWith("__draft_automation");
}

interface DraftAutomationCardProps {
  chatId?: string;
  metadata?: string;
}

function DraftAutomationCardComponent({ chatId, metadata }: DraftAutomationCardProps) {
  const { data: chat } = useChat(chatId);
  const draft = draftFromToolResult(metadata);
  if (!draft) return null;

  const review = () =>
    openAutomationDialog({
      projectId: chat?.projectId,
      workflowRef: draft.workflow || undefined,
      custom: true,
      prefill: draftToPrefill(draft, { projectId: chat?.projectId }),
    });

  return (
    <div data-testid="draft-automation-card" className="my-1.5 rounded-lg border border-border bg-card px-3 py-2.5">
      <div className="flex items-start gap-2.5">
        <CalendarClock className="mt-0.5 h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden="true" />
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-foreground">Automation drafted</p>
          <p className="mt-0.5 break-words text-sm text-muted-foreground">{draft.summary}</p>
          <p className="mt-0.5 text-xs text-muted-foreground">Nothing runs until you review it and press Create.</p>
        </div>
        <Button size="sm" onClick={review} data-testid="draft-automation-review">
          Review &amp; create
        </Button>
      </div>
    </div>
  );
}

export const DraftAutomationCard = memo(DraftAutomationCardComponent);

/** The tool-content router's entry, for surfaces that render tool content. */
function DraftAutomationToolRendererComponent({ ctx }: ToolContentProps) {
  return <DraftAutomationCard chatId={ctx.chatId} metadata={ctx.result?.metadata} />;
}

export const DraftAutomationToolRenderer = memo(DraftAutomationToolRendererComponent);
