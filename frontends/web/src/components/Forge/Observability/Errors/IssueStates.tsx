// Copyright (c) 2025 Reliant Labs

/**
 * The Errors view's own states beyond the shared ones (ObsStates): a control
 * plane without issues, and an issue that is not there.
 */

import type { ReactNode } from "react";
import { Bug, SearchX } from "lucide-react";

import Button from "@/components/forge-ui/button";

function Note({ icon, title, children, testId, action }: { icon: ReactNode; title: string; children: ReactNode; testId: string; action?: ReactNode }) {
  return (
    <div role="status" data-testid={testId} className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5">
      <span className="mt-0.5 shrink-0 text-ink-muted" aria-hidden="true">
        {icon}
      </span>
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium text-ink">{title}</p>
        <div className="text-xs text-ink-muted">{children}</div>
        {action && <div className="pt-1">{action}</div>}
      </div>
    </div>
  );
}

/** Unimplemented, or Unavailable "not configured": this control plane does not group errors into issues (yet). */
export function IssuesNotConfigured() {
  return (
    <Note icon={<Bug className="h-4 w-4" />} title="Error issues aren't available on this control plane" testId="obs-errors-not-configured">
      <p>
        It doesn&apos;t group errors into issues yet. Error logs and failed spans are still in Logs and Traces; issues appear here once the
        control plane runs the issues service.
      </p>
    </Note>
  );
}

export function IssueNotFound({ onBack }: { onBack: () => void }) {
  return (
    <Note
      icon={<SearchX className="h-4 w-4" />}
      title="That issue isn't here"
      testId="obs-issue-not-found"
      action={
        <Button variant="outline" size="sm" onClick={onBack} data-testid="obs-issue-not-found-back">
          Back to the list
        </Button>
      }
    >
      <p>It may have been merged into another issue, or it belongs to an organization you are not in.</p>
    </Note>
  );
}
