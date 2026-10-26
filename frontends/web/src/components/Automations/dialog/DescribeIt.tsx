// Copyright (c) 2025 Reliant Labs

/**
 * "Describe it": a one-line box at the top of New automation (report 03,
 * §6.7 option B). The person writes what they want — "every weekday at 9,
 * summarise yesterday's failed CI runs" — and the server drafts the WHEN and
 * the prompt, which fill the form below for review. Nothing is created here:
 * the draft is only ever starting values, and Create is still the person's.
 */

import { useId, useState, type FormEvent, type KeyboardEvent } from "react";
import { Sparkles } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { triggerErrorMessage, type AutomationDraft } from "@/hooks/trigger-model";
import { draftAutomation } from "@/hooks/trigger-queries";
import { errorTextClass, fieldClass, hintClass } from "../../workflow/run/runFormStyles";
import { draftToPrefill } from "./draftToPrefill";
import type { AutomationPrefill } from "./openAutomationDialog";
import { browserTimezone } from "./whenModel";

export interface DescribeItProps {
  projectId: string;
  /** The workflow it must run, when the dialog is for one. */
  workflow?: string;
  onDraft: (prefill: AutomationPrefill, summary: string) => void;
}

export function DescribeIt({ projectId, workflow, onDraft }: DescribeItProps) {
  const id = useId();
  const [text, setText] = useState("");
  const [drafting, setDrafting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [drafted, setDrafted] = useState<AutomationDraft | null>(null);

  const draft = async (event?: FormEvent) => {
    event?.preventDefault();
    const description = text.trim();
    if (!description || drafting || !projectId) return;
    setDrafting(true);
    setError(null);
    try {
      const result = await draftAutomation({ description, projectId, workflow, timezone: browserTimezone() });
      setDrafted(result);
      onDraft(draftToPrefill(result), result.summary);
    } catch (err) {
      setError(triggerErrorMessage(err));
    } finally {
      setDrafting(false);
    }
  };

  // The box sits inside the dialog's form: Enter drafts rather than creating.
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      void draft();
    }
  };

  return (
    <div className="space-y-1.5" data-testid="describe-it">
      <label htmlFor={`${id}-describe`} className="sr-only">
        Describe the automation
      </label>
      <div className="flex gap-2">
        <input
          id={`${id}-describe`}
          className={fieldClass}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="Describe it: every weekday at 9, summarise yesterday's failed CI runs"
          autoComplete="off"
          aria-describedby={`${id}-describe-hint`}
          disabled={drafting}
        />
        <Button
          type="button"
          variant="outline"
          onClick={() => void draft()}
          loading={drafting}
          disabled={!text.trim() || !projectId}
          leftIcon={<Sparkles className="h-3.5 w-3.5" />}
        >
          Draft it
        </Button>
      </div>
      <p id={`${id}-describe-hint`} className={hintClass} aria-live="polite">
        {drafted
          ? `Drafted: ${drafted.summary}. Check it below; nothing is created until you press Create.`
          : "Reliant fills in the form for you to check. Or fill it in yourself."}
      </p>
      {error && (
        <p className={errorTextClass} role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
