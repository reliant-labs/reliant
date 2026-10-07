// Copyright (c) 2025 Reliant Labs

/**
 * The integration actions you told Reliant to always allow, and the way to
 * take that back.
 *
 * In a chat you are in, an integration action that changes something —
 * posting to Slack, sending an email, opening an issue, an HTTP request — asks
 * first (ActionApprovalCard). "Always allow" on that card is remembered per
 * user and per action as one settings row, `tool.approval.<tool>`, written by
 * the server when you approve (ApprovalService.Approve) and read before every
 * ask (ApprovalCreate). Revoking deletes the row, so the next call asks again.
 *
 * The row's value carries the action's labels, copied from its manifest when
 * it was written, so this list needs no catalog lookup.
 */

import { useCallback, useEffect, useState } from "react";
import { Loader2 } from "lucide-react";

import { api } from "../../api/client";
import { IntegrationLogoTile } from "../icons/IntegrationLogo";

/** Settings key prefix for an action's "always allow" (tools.ActionApprovalSettingPrefix). */
export const ACTION_APPROVAL_SETTING_PREFIX = "tool.approval.";

export interface AllowedAction {
  key: string;
  tool: string;
  displayName: string;
  integration?: string;
  icon?: string;
}

/**
 * The "always allow" rows among a user's settings. A row whose value does not
 * parse is listed by its tool name, so it can still be revoked.
 */
export function allowedActions(settings: Array<{ key: string; value: string }>): AllowedAction[] {
  return settings
    .filter((s) => s.key.startsWith(ACTION_APPROVAL_SETTING_PREFIX))
    .map((s) => {
      const tool = s.key.slice(ACTION_APPROVAL_SETTING_PREFIX.length);
      let parsed: { display_name?: string; integration?: string; icon?: string } = {};
      try {
        const value: unknown = JSON.parse(s.value);
        if (value && typeof value === "object") parsed = value as typeof parsed;
      } catch {
        // Listed by tool name.
      }
      return {
        key: s.key,
        tool,
        displayName: parsed.display_name || tool,
        integration: parsed.integration,
        icon: parsed.icon,
      };
    })
    .sort((a, b) => `${a.integration ?? ""} ${a.displayName}`.localeCompare(`${b.integration ?? ""} ${b.displayName}`));
}

export function ActionApprovalSettings() {
  const [actions, setActions] = useState<AllowedAction[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [revoking, setRevoking] = useState<string | null>(null);
  const [revokeError, setRevokeError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    // Read fresh rather than from the settings cache: the server writes these
    // when you answer a card, so the cache may not have them yet.
    api.settings
      .listSettings()
      .then(({ settings }) => {
        if (!cancelled) setActions(allowedActions(settings));
      })
      .catch(() => {
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const revoke = useCallback(async (action: AllowedAction) => {
    setRevoking(action.key);
    setRevokeError(null);
    try {
      await api.settings.deleteSetting(action.key);
      setActions((prev) => prev?.filter((a) => a.key !== action.key) ?? prev);
    } catch {
      setRevokeError(`Couldn't revoke ${action.displayName}. Try again.`);
    } finally {
      setRevoking(null);
    }
  }, []);

  return (
    <section aria-labelledby="action-approvals-heading" className="space-y-3">
      <div>
        <h3 id="action-approvals-heading" className="text-sm font-semibold text-foreground">
          Integration actions
        </h3>
        <p className="mt-1 text-xs text-muted-foreground">
          In a chat you&apos;re in, Reliant asks before an integration action changes something —
          posting a message, sending an email, opening an issue. These are the actions you chose to
          always allow; they run without asking until you revoke them.
        </p>
      </div>

      {failed ? (
        <p className="text-xs text-destructive-ink">Couldn&apos;t load your allowed actions. Reload to try again.</p>
      ) : actions === null ? (
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
          Loading allowed actions…
        </div>
      ) : actions.length === 0 ? (
        <p className="rounded-lg border border-border/60 bg-background p-3 text-xs text-muted-foreground">
          Nothing is always allowed. Every integration action that changes something asks first.
        </p>
      ) : (
        <ul className="divide-y divide-border/60 rounded-lg border border-border/60 bg-background">
          {actions.map((action) => (
            <li key={action.key} className="flex items-center gap-3 px-3 py-2">
              <IntegrationLogoTile icon={action.icon} size="sm" />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm text-foreground">{action.displayName}</p>
                <p className="truncate text-xs text-muted-foreground">
                  {action.integration ? `${action.integration} · ` : ""}
                  <code className="font-mono">{action.tool}</code>
                </p>
              </div>
              <button
                type="button"
                onClick={() => void revoke(action)}
                disabled={revoking === action.key}
                aria-label={`Revoke always allow for ${action.displayName}${action.integration ? ` (${action.integration})` : ""}`}
                className="h-7 rounded-md border border-border px-2.5 text-xs font-medium text-foreground transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60"
              >
                {revoking === action.key ? "Revoking…" : "Revoke"}
              </button>
            </li>
          ))}
        </ul>
      )}
      {revokeError && (
        <p role="alert" className="text-xs text-destructive-ink">
          {revokeError}
        </p>
      )}
    </section>
  );
}
