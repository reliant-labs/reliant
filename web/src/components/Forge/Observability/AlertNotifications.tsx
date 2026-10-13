// Copyright (c) 2025 Reliant Labs

/**
 * The in-app notifications bell: what the `reliant-inapp` receiver delivered
 * to this organization, newest first, with an unread count.
 *
 * Notifications are org-wide (a route in any environment can deliver here), so
 * the list is not narrowed by the environments in view. Any member may mark
 * them read. Until alerting runs for the org there is nothing to deliver, and
 * the bell is not shown at all rather than sitting at zero forever.
 */

import { useState } from "react";
import { Bell, CheckCheck } from "lucide-react";

import { useAlertingMutation, useNotifications } from "@/hooks/observability-queries";
import type { Notification } from "@/services/controlPlane/observability/alertingTypes";
import { cn } from "@/lib/utils";

import { SeverityChip } from "./AlertsView";
import { formatAgo, formatTimestamp } from "./format";
import { Popover } from "./ObsScopeBar";

export interface AlertNotificationsProps {
  orgId: string;
  /** Environment id → name, for the ones this place knows. */
  envName: (id: string) => string | undefined;
  onOpenAlerts: () => void;
}

export function AlertNotifications({ orgId, envName, onOpenAlerts }: AlertNotificationsProps) {
  const [open, setOpen] = useState(false);
  const notifications = useNotifications(orgId);
  const mark = useAlertingMutation(orgId, (api, org, ids: string[]) => api.markNotificationsRead(org, ids));
  if (!notifications.data) return null;
  const { unread, notifications: list } = notifications.data;

  return (
    <Popover
      label="Alert notifications"
      open={open}
      onOpenChange={setOpen}
      testId="obs-notifications"
      triggerLabel={unread ? `Alert notifications, ${unread} unread` : "Alert notifications"}
      triggerClassName="relative inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      panelClassName="right-0 w-96 max-w-[calc(100vw-2rem)]"
      trigger={
        <>
          <Bell className="h-4 w-4" aria-hidden="true" />
          {unread > 0 && (
            <span
              className="absolute -right-0.5 -top-0.5 min-w-[1.1rem] rounded-full bg-destructive px-1 text-center text-2xs font-semibold leading-[1.1rem] text-destructive-foreground tabular-nums"
              data-testid="obs-notifications-unread"
            >
              {unread > 99 ? "99+" : unread}
            </span>
          )}
        </>
      }
    >
      <div className="flex items-center justify-between gap-2 px-1 pb-2">
        <h3 className="text-sm font-medium text-foreground">Notifications</h3>
        {unread > 0 && (
          <button
            type="button"
            onClick={() => mark.mutate([])}
            disabled={mark.isPending}
            className="inline-flex items-center gap-1 rounded-sm text-xs text-primary underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60"
            data-testid="obs-notifications-mark-all"
          >
            <CheckCheck className="h-3.5 w-3.5" aria-hidden="true" />
            Mark all read
          </button>
        )}
      </div>
      {list.length === 0 ? (
        <p className="px-1 py-4 text-center text-xs text-muted-foreground" data-testid="obs-notifications-none">
          Nothing has been delivered in-app yet. Alerts routed to <code className="font-mono">reliant-inapp</code> show up here.
        </p>
      ) : (
        <ul className="-mx-2 max-h-96 divide-y divide-border/60 overflow-y-auto border-y border-border/60">
          {list.map((n) => (
            <NotificationItem key={n.id} notification={n} env={envName(n.envId)} onRead={() => mark.mutate([n.id])} />
          ))}
        </ul>
      )}
      <div className="px-1 pt-2">
        <button
          type="button"
          onClick={() => {
            setOpen(false);
            onOpenAlerts();
          }}
          className="rounded-sm text-xs text-primary underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="obs-notifications-open-alerts"
        >
          See what is firing
        </button>
      </div>
    </Popover>
  );
}

function NotificationItem({ notification: n, env, onRead }: { notification: Notification; env: string | undefined; onRead: () => void }) {
  const summary = n.annotations.summary || n.annotations.description || "";
  return (
    <li className={cn("flex items-start gap-2 px-3 py-2.5", !n.read && "bg-primary/5")} data-testid={`obs-notification-${n.id}`} data-read={n.read}>
      <span
        className={cn("mt-1.5 h-2 w-2 shrink-0 rounded-full", n.read ? "bg-transparent" : n.status === "firing" ? "bg-destructive" : "bg-success")}
        aria-hidden="true"
      />
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="flex flex-wrap items-center gap-1.5">
          <span className={cn("text-2xs font-medium uppercase tracking-wide", n.status === "firing" ? "text-destructive" : "text-success")}>
            {n.status}
          </span>
          <span className="font-mono text-xs font-medium text-foreground">{n.alertname}</span>
          <SeverityChip severity={n.severity} />
          {env && <span className="font-mono text-2xs text-muted-foreground">{env}</span>}
        </div>
        {summary && <p className="text-xs text-foreground">{summary}</p>}
        <p className="text-2xs text-muted-foreground" title={formatTimestamp(n.receivedAtMs)}>
          {formatAgo(n.receivedAtMs)}
        </p>
      </div>
      {!n.read && (
        <button
          type="button"
          onClick={onRead}
          className="shrink-0 rounded-sm text-2xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label={`Mark ${n.alertname} read`}
          data-testid={`obs-notification-read-${n.id}`}
        >
          Mark read
        </button>
      )}
    </li>
  );
}
