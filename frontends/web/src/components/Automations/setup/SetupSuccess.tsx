// Copyright (c) 2025 Reliant Labs

/**
 * What the setup dialog says once an automation is on: that it is on, where,
 * what will make it run next — one sentence per trigger kind, from data — the
 * warnings that did not block it, and what to do now (try it, open the
 * workflow, done). A webhook's URL and token come back once, here, except for
 * one Reliant connected itself (Trigger.webhookManaged), which has neither.
 */

import { CircleCheck, AlertTriangle } from "lucide-react";

import type { EnableWorkflowResult } from "@/hooks/trigger-model";
import { useIntegrationEventNaming } from "@/hooks/catalog-queries";
import { describeSchedule } from "@/lib/cronText";
import { describeDeclaredSource, integrationOf, scheduleOf, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { formatAbsoluteTime } from "@/lib/relativeTime";
import Button from "@/components/forge-ui/button";
import { WebhookCredentialPanel } from "../dialog/WebhookCredentialPanel";
import { TestEventPreview } from "../dialog/TestEventPreview";

export interface SetupSuccessProps {
  workflowTitle: string;
  projectName: string;
  /** "Saved" wording for a settings change, "on" for a new setup. */
  saved: boolean;
  result: EnableWorkflowResult;
  declared: readonly DeclaredTrigger[];
  /** What the scope inputs hold, joined ("acme/api or acme/web"). */
  scopeSummary?: string;
  warnings?: readonly string[];
  onDone: () => void;
  onTryNow?: () => void;
  onOpenWorkflow?: () => void;
}

/** "acme/api, acme/web or acme/docs". */
export function joinOr(values: readonly string[]): string {
  if (values.length <= 1) return values[0] ?? "";
  return `${values.slice(0, -1).join(", ")} or ${values[values.length - 1]}`;
}

export function SetupSuccess({
  workflowTitle,
  projectName,
  saved,
  result,
  declared,
  scopeSummary,
  warnings = [],
  onDone,
  onTryNow,
  onOpenWorkflow,
}: SetupSuccessProps) {
  const naming = useIntegrationEventNaming();
  const hooks = result.results.filter((r) => r.activation && r.webhook);
  const sentences = declared.map((trigger) => {
    const activation = result.results.find((r) => r.trigger === trigger.name)?.activation;
    if (scheduleOf(trigger)) {
      const when = activation?.nextFireAt ? formatAbsoluteTime(activation.nextFireAt) : describeDeclaredSource(trigger, describeSchedule, naming);
      return { key: trigger.name ?? "", text: `Next run ${when}.` };
    }
    if (integrationOf(trigger)) {
      const what = describeDeclaredSource(trigger, describeSchedule, naming);
      return { key: trigger.name ?? "", text: `Runs on the next event (${what})${scopeSummary ? ` in ${scopeSummary}` : ""}.` };
    }
    if (activation?.webhookManaged) {
      return {
        key: trigger.name ?? "",
        text: `Connected to your organization's alerts: the next critical or error alert in ${projectName}'s environments starts a run. Turning it off disconnects it.`,
      };
    }
    return { key: trigger.name ?? "", text: `${describeDeclaredSource(trigger, describeSchedule, naming)}.` };
  });

  return (
    <div className="space-y-4" data-testid="setup-success">
      <div className="flex items-start gap-3">
        <CircleCheck className="mt-0.5 h-5 w-5 shrink-0 text-success-ink" aria-hidden />
        <div className="min-w-0">
          <h3 className="text-base font-semibold text-foreground" role="status">
            {saved ? `${workflowTitle} in ${projectName} is saved` : `${workflowTitle} is on in ${projectName}`}
          </h3>
          <ul className="mt-1.5 space-y-1 text-sm text-muted-foreground" aria-label="What happens next">
            {sentences.map((s) => (
              <li key={s.key}>{s.text}</li>
            ))}
          </ul>
        </div>
      </div>

      {warnings.length > 0 && (
        <ul className="space-y-1 rounded-md border border-warning-border bg-warning-surface px-3 py-2 text-sm text-warning-ink" aria-label="Warnings">
          {warnings.map((w) => (
            <li key={w} className="flex items-start gap-2">
              <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
              {w}
            </li>
          ))}
        </ul>
      )}

      {hooks.map((r) => (
        <div key={r.trigger} className="space-y-2">
          <p className="text-sm font-medium text-foreground">
            Webhook for <span className="font-mono">{r.trigger}</span>
          </p>
          <WebhookCredentialPanel trigger={r.activation!} credential={r.webhook} />
        </div>
      ))}

      {result.results.filter((r) => r.activation && integrationOf(declared.find((d) => d.name === r.trigger) ?? {})).map((r) => (
        <TestEventPreview key={r.trigger} trigger={r.activation!} />
      ))}

      <div className="flex flex-wrap justify-end gap-2 border-t border-border pt-4">
        {onOpenWorkflow && (
          <Button variant="outline" onClick={onOpenWorkflow}>
            Open workflow
          </Button>
        )}
        {onTryNow && (
          <Button variant="outline" onClick={onTryNow}>
            Try it now
          </Button>
        )}
        <Button variant="primary" onClick={onDone}>
          Done
        </Button>
      </div>
    </div>
  );
}
