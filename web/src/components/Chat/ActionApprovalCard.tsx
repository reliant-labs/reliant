// Copyright (c) 2025 Reliant Labs

/**
 * ActionApprovalCard — the question asked before an integration action that
 * changes something runs in a chat you are in: posting to Slack, sending an
 * email or a text, opening or commenting on an issue, an HTTP request.
 *
 * The server raises one tool approval per call (the runtime's action approval
 * gate) and the agent waits on the answer:
 *   Allow once    the call runs.
 *   Always allow  the call runs, and that action stops asking — until the
 *                 decision is revoked in Settings → AI → Tools.
 *   Deny          the call is refused and recorded as failed; the agent is
 *                 told the user did not approve it.
 *
 * Focus: the card takes focus when it appears and nothing else has it (it
 * never pulls focus out of the composer), so a keyboard user lands on the
 * decision; Tab reaches the three buttons. Focus goes to the card itself,
 * never a button, so a stray Enter cannot answer.
 */

import { useEffect, useId, useRef } from "react";
import { ShieldQuestion } from "lucide-react";

import type { ToolApprovalRequest } from "../../api/approval-grpc";
import { ALLOW_ONCE_ACTION, ALWAYS_ALLOW_ACTION } from "../../api/approval-grpc";
import { useApproveToolRequest, useDenyToolRequest } from "../../hooks/approval-queries";
import { useSurface } from "../../lib/surfaceContext";
import { cn } from "../../lib/utils";
import { IntegrationLogoTile } from "../icons/IntegrationLogo";

interface ActionApprovalCardProps {
  approval: ToolApprovalRequest;
  chatId: string;
  /** "⌘" or "Ctrl" when the global approve shortcut answers this card. */
  shortcutKey?: string;
}

/** One parameter value as written: text as is, anything else as JSON. */
function formatParam(value: unknown): string {
  if (typeof value === "string") return value;
  return JSON.stringify(value, null, 2);
}

export function ActionApprovalCard({ approval, chatId, shortcutKey }: ActionApprovalCardProps) {
  const surface = useSurface();
  const isNarrow = surface !== "desktop";
  const titleId = useId();
  const paramsId = useId();
  const alwaysHintId = useId();
  const cardRef = useRef<HTMLElement>(null);

  const approve = useApproveToolRequest();
  const deny = useDenyToolRequest();
  const busy = approve.isPending || deny.isPending;

  const actionName = approval.tool_name ?? "this action";
  const title = approval.title || `Run ${actionName}?`;
  const params = approval.params;

  // Take focus only when nothing has it — never from the composer, another
  // card or any other control the person is using.
  useEffect(() => {
    const active = document.activeElement;
    if (!active || active === document.body) cardRef.current?.focus();
  }, []);

  const answer = (kind: "once" | "always" | "deny") => {
    if (busy) return;
    if (kind === "deny") {
      deny.mutate({ chatId, requestId: approval.id, actionTaken: "deny" });
    } else {
      approve.mutate({
        chatId,
        requestId: approval.id,
        actionTaken: kind === "always" ? ALWAYS_ALLOW_ACTION : ALLOW_ONCE_ACTION,
      });
    }
  };

  const buttonSize = isNarrow ? "min-h-[44px] px-3 text-sm" : "h-8 px-3 text-sm";
  const focusRing = "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-card";

  return (
    <section
      ref={cardRef}
      tabIndex={-1}
      aria-labelledby={titleId}
      aria-describedby={params !== undefined ? paramsId : undefined}
      data-testid="action-approval-card"
      className={cn(
        "mb-3 w-full max-w-[720px] rounded-lg border border-border bg-card p-3 elevation-3",
        "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
      )}
    >
      <div className="flex items-start gap-3">
        <IntegrationLogoTile icon={approval.integration_icon} size="md" />
        <div className="min-w-0 flex-1">
          <p className="flex items-center gap-1 text-xs text-muted-foreground">
            <ShieldQuestion className="h-3.5 w-3.5" aria-hidden />
            {approval.integration_name ? `${approval.integration_name} · ` : ""}Needs your approval
          </p>
          <h3 id={titleId} className="mt-0.5 break-words text-sm font-semibold text-foreground">
            {title}
          </h3>
        </div>
      </div>

      {params !== undefined && (
        <div
          id={paramsId}
          className="mt-3 max-h-56 overflow-auto rounded-md border border-border/60 bg-background p-2"
        >
          <p className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Parameters
          </p>
          {typeof params === "string" ? (
            <pre className="whitespace-pre-wrap break-words font-mono text-xs text-foreground">{params}</pre>
          ) : (
            <dl className="space-y-1.5">
              {Object.entries(params).map(([key, value]) => (
                <div key={key} className="grid grid-cols-[minmax(5rem,auto)_1fr] gap-x-3">
                  <dt className="font-mono text-xs text-muted-foreground">{key}</dt>
                  <dd className="min-w-0">
                    <pre className="whitespace-pre-wrap break-words font-mono text-xs text-foreground">
                      {formatParam(value)}
                    </pre>
                  </dd>
                </div>
              ))}
            </dl>
          )}
        </div>
      )}

      <p id={alwaysHintId} className="sr-only">
        Runs {actionName} now and stops asking about it. Revoke in Settings, AI, Tools.
      </p>
      <div className={cn("mt-3 flex items-center gap-2", isNarrow && "flex-col items-stretch")}>
        <button
          type="button"
          onClick={() => answer("once")}
          disabled={busy}
          className={cn(
            "inline-flex items-center justify-center gap-2 rounded-md bg-primary font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-60",
            buttonSize,
            focusRing,
          )}
        >
          Allow once
          {!isNarrow && shortcutKey && (
            <kbd className="rounded bg-primary-foreground/20 px-1.5 py-0.5 font-mono text-xs" aria-hidden>
              {shortcutKey}+↵
            </kbd>
          )}
        </button>
        <button
          type="button"
          onClick={() => answer("always")}
          disabled={busy}
          aria-describedby={alwaysHintId}
          title={`Stop asking about ${actionName}. Revoke in Settings → AI → Tools.`}
          className={cn(
            "inline-flex items-center justify-center rounded-md border border-border bg-background font-medium text-foreground transition-colors hover:bg-accent hover:text-accent-foreground disabled:opacity-60",
            buttonSize,
            focusRing,
          )}
        >
          Always allow
        </button>
        <button
          type="button"
          onClick={() => answer("deny")}
          disabled={busy}
          className={cn(
            "inline-flex items-center justify-center rounded-md border border-destructive/30 bg-destructive/10 font-medium text-destructive-ink transition-colors hover:bg-destructive/20 disabled:opacity-60",
            !isNarrow && "ml-auto",
            buttonSize,
            focusRing,
          )}
        >
          Deny
        </button>
      </div>
    </section>
  );
}
