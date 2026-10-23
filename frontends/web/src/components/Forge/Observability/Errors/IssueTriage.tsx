// Copyright (c) 2025 Reliant Labs

/**
 * Triage one issue: resolve (now, in the next release, or in a named
 * version), ignore (for a while, or until unignored), or reopen.
 *
 * ── OPTIMISTIC CONCURRENCY, SAID OUT LOUD ────────────────────────────────────
 *
 * Every request echoes the Issue.version the reader is looking at. If someone
 * (or the issues worker: a regression) changed the issue since, the server
 * refuses with Aborted and changes nothing. The view then re-reads the issue
 * and SAYS what changed — "it is resolved in v1.8.4 now, not unresolved" — and
 * leaves the next move to the reader. It never re-sends the request with the
 * new version: that would overwrite a decision the reader has not seen, which
 * is exactly what the version exists to prevent.
 */

import { useState } from "react";
import { AlertTriangle, BellOff, CheckCircle2, RotateCcw } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { useUpdateIssueStatus } from "@/hooks/observability-queries";
import { issuesErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import type { Issue } from "@/services/controlPlane/observability/issuesTypes";

import { conflictSummary, IGNORE_FOR, ignoreChange, resolveChange, triageActionsOf, type IgnoreFor, type ResolveChoice } from "./issueModel";

const FIELD = "h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function IssueTriage({ issue }: { issue: Issue }) {
  const update = useUpdateIssueStatus();
  const [resolveIn, setResolveIn] = useState<ResolveChoice>("now");
  const [version, setVersion] = useState("");
  const [ignoreFor, setIgnoreFor] = useState<IgnoreFor>("forever");
  /** The issue as it was when the reader clicked: what a conflict is measured against. */
  const [readAtClick, setReadAtClick] = useState<Issue | null>(null);

  const actions = triageActionsOf(issue.status);
  const errorKind = update.error ? issuesErrorKind(update.error) : null;
  const conflict = errorKind === "conflict" && readAtClick;
  // Once the re-read issue carries a new version, the conflict note can say what changed.
  const refreshed = conflict && issue.version !== readAtClick.version;

  const send = (change: Parameters<typeof update.mutate>[0]["change"] | null) => {
    if (!change) return;
    setReadAtClick(issue);
    update.mutate({ issueId: issue.id, envId: issue.envId, change, expectedVersion: issue.version });
  };
  const resolve = resolveChange(resolveIn, version);

  return (
    <div className="space-y-2" data-testid="obs-issue-triage" data-version={issue.version}>
      <div className="flex flex-wrap items-center gap-2">
        {actions.includes("resolve") && (
          <div className="inline-flex flex-wrap items-center gap-1.5" role="group" aria-label="Resolve">
            <Button
              size="sm"
              variant="primary"
              leftIcon={<CheckCircle2 className="h-3.5 w-3.5" />}
              onClick={() => send(resolve)}
              disabled={!resolve || update.isPending}
              loading={update.isPending && update.variables?.change.status === "resolved"}
              data-testid="obs-issue-resolve"
            >
              Resolve
            </Button>
            <label htmlFor="obs-issue-resolve-in" className="sr-only">
              Resolve in
            </label>
            <select id="obs-issue-resolve-in" value={resolveIn} onChange={(e) => setResolveIn(e.target.value as ResolveChoice)} className={FIELD} data-testid="obs-issue-resolve-in">
              <option value="now">now</option>
              <option value="next_release">in the next release</option>
              <option value="version">in version…</option>
            </select>
            {resolveIn === "version" && (
              <>
                <label htmlFor="obs-issue-resolve-version" className="sr-only">
                  Version
                </label>
                <input
                  id="obs-issue-resolve-version"
                  value={version}
                  onChange={(e) => setVersion(e.target.value)}
                  placeholder="v1.8.4"
                  className={`${FIELD} w-28 font-mono`}
                  data-testid="obs-issue-resolve-version"
                />
              </>
            )}
          </div>
        )}
        {actions.includes("ignore") && (
          <div className="inline-flex flex-wrap items-center gap-1.5" role="group" aria-label="Ignore">
            <Button
              size="sm"
              variant="outline"
              leftIcon={<BellOff className="h-3.5 w-3.5" />}
              onClick={() => send(ignoreChange(ignoreFor))}
              disabled={update.isPending}
              loading={update.isPending && update.variables?.change.status === "ignored"}
              data-testid="obs-issue-ignore"
            >
              Ignore
            </Button>
            <label htmlFor="obs-issue-ignore-for" className="sr-only">
              Ignore for
            </label>
            <select id="obs-issue-ignore-for" value={ignoreFor} onChange={(e) => setIgnoreFor(e.target.value as IgnoreFor)} className={FIELD} data-testid="obs-issue-ignore-for">
              {IGNORE_FOR.map((option) => (
                <option key={option.id} value={option.id}>
                  {option.label}
                </option>
              ))}
            </select>
          </div>
        )}
        {actions.includes("unresolve") && (
          <Button
            size="sm"
            variant="outline"
            leftIcon={<RotateCcw className="h-3.5 w-3.5" />}
            onClick={() => send({ status: "unresolved" })}
            disabled={update.isPending}
            loading={update.isPending}
            data-testid="obs-issue-unresolve"
          >
            Reopen
          </Button>
        )}
      </div>
      {conflict ? (
        <p role="alert" className="flex items-start gap-2 rounded-md border border-warning/40 bg-background px-3 py-2 text-xs text-foreground" data-testid="obs-issue-conflict">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning" aria-hidden="true" />
          <span>
            Someone changed this issue while you were looking, so nothing was changed.{" "}
            {refreshed ? `${conflictSummary(readAtClick, issue)} Check it, then choose again.` : "Loading what changed…"}
          </span>
        </p>
      ) : update.error ? (
        <p role="alert" className="text-xs text-destructive" data-testid="obs-issue-triage-error">
          {errorKind === "denied"
            ? "You can read this issue but not triage it: triage needs a member of the organization, or a credential with obs:triage."
            : errorKind === "not-found"
              ? "That issue is gone (merged, or no longer yours to see)."
              : obsErrorMessage(update.error) || "The triage didn't go through. Try again."}
        </p>
      ) : null}
    </div>
  );
}
