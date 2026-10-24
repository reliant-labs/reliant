// Copyright (c) 2025 Reliant Labs

/**
 * Silences: pause the notifications of matching alerts until a time.
 *
 * A silence only ever mutes THIS organization: control-plane adds the
 * `reliant_org_id` matcher itself and refuses one of ours on any `reliant_*`
 * label, so the list shows that matcher as a fixed "this organization" scope
 * rather than as something the reader wrote. Creating and expiring need the
 * org admin role; a member sees the list read-only.
 */

import { useId, useState } from "react";
import { BellOff, Plus, Trash2 } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Modal from "@/components/forge-ui/modal";
import Button from "@/components/forge-ui/button";
import { useAlertingMutation, useSilences } from "@/hooks/observability-queries";
import type { CreateSilenceInput, Matcher, Silence } from "@/services/controlPlane/observability/alertingTypes";
import { alertingErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import { cn } from "@/lib/utils";

import { formatAgo, formatWhen } from "./format";
import { ObsError, ObsLoadingRows } from "./ObsStates";

const FIELD =
  "h-8 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** The label the server scopes every silence by; shown, never editable. */
const ORG_MATCHER = "reliant_org_id";

/** How a matcher reads: `=`, `!=`, `=~`, `!~` as Alertmanager writes them. */
export function matcherOp(m: Pick<Matcher, "isRegex" | "isEqual">): string {
  if (m.isRegex) return m.isEqual ? "=~" : "!~";
  return m.isEqual ? "=" : "!=";
}

export function matcherText(m: Matcher): string {
  return `${m.name}${matcherOp(m)}"${m.value}"`;
}

const DURATIONS: [string, number][] = [
  ["1 hour", 60],
  ["4 hours", 240],
  ["1 day", 24 * 60],
  ["1 week", 7 * 24 * 60],
];

export function AlertSilences({ orgId, readOnly, onDenied }: { orgId: string; readOnly: boolean; onDenied: () => void }) {
  const [includeExpired, setIncludeExpired] = useState(false);
  const [creating, setCreating] = useState(false);
  const silences = useSilences(orgId, includeExpired);
  const expire = useAlertingMutation(orgId, (api, org, id: string) => api.expireSilence(org, id));

  if (silences.isLoading) return <ObsLoadingRows testId="obs-silences-loading" />;
  if (silences.error) return <ObsError error={silences.error} subject="this organization" onRetry={() => void silences.refetch()} />;
  const list = silences.data ?? [];
  const expireError = expire.error && alertingErrorKind(expire.error) !== "denied" ? obsErrorMessage(expire.error) : "";

  return (
    <div className="space-y-3" data-testid="obs-silences">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="primary" size="sm" leftIcon={<Plus className="h-3.5 w-3.5" />} disabled={readOnly} onClick={() => setCreating(true)} data-testid="obs-silence-new">
          New silence
        </Button>
        <label className="inline-flex items-center gap-2 text-xs text-foreground">
          <input
            type="checkbox"
            checked={includeExpired}
            onChange={(e) => setIncludeExpired(e.target.checked)}
            className="h-3.5 w-3.5 accent-[hsl(var(--primary))]"
            data-testid="obs-silences-expired"
          />
          Show expired
        </label>
        {expireError && (
          <p role="alert" className="text-xs text-destructive">
            {expireError}
          </p>
        )}
      </div>
      {list.length === 0 ? (
        <div className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-background px-4 py-5" role="status" data-testid="obs-silences-none">
          <BellOff className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <div className="space-y-1">
            <p className="text-sm font-medium text-foreground">No {includeExpired ? "" : "active "}silences</p>
            <p className="text-xs text-muted-foreground">
              Silence an alert during planned work so it stops notifying. Every silence ends on its own at the time you set, and only ever
              applies to this organization.
            </p>
          </div>
        </div>
      ) : (
        <Card padding="none">
          <ul className="divide-y divide-border/60">
            {list.map((s) => (
              <SilenceRow
                key={s.id}
                silence={s}
                readOnly={readOnly}
                expiring={expire.isPending && expire.variables === s.id}
                onExpire={() =>
                  expire.mutate(s.id, {
                    onError: (error) => {
                      if (alertingErrorKind(error) === "denied") onDenied();
                    },
                  })
                }
              />
            ))}
          </ul>
        </Card>
      )}
      {creating && <SilenceDialog orgId={orgId} title="New silence" onClose={() => setCreating(false)} onDenied={onDenied} onCreated={() => setCreating(false)} />}
    </div>
  );
}

function SilenceRow({ silence, readOnly, expiring, onExpire }: { silence: Silence; readOnly: boolean; expiring: boolean; onExpire: () => void }) {
  const own = silence.matchers.filter((m) => m.name !== ORG_MATCHER);
  const now = Date.now();
  return (
    <li className="flex flex-wrap items-start gap-3 px-4 py-3" data-testid={`obs-silence-${silence.id}`} data-state={silence.state}>
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-1.5">
          <span
            className={cn(
              "rounded px-1.5 py-px text-2xs font-medium uppercase tracking-wide ring-1 ring-inset",
              silence.state === "active" ? "bg-primary/10 text-primary ring-primary/30" : "text-muted-foreground ring-border"
            )}
          >
            {silence.state}
          </span>
          {own.map((m, i) => (
            <code key={i} className="rounded bg-background px-1.5 py-px font-mono text-2xs text-foreground ring-1 ring-inset ring-border/60">
              {matcherText(m)}
            </code>
          ))}
          <span className="rounded px-1.5 py-px text-2xs text-muted-foreground ring-1 ring-inset ring-border/60" title={`${ORG_MATCHER}, added by Reliant`}>
            this organization only
          </span>
        </div>
        <p className="text-sm text-foreground">{silence.comment}</p>
        <p className="text-xs text-muted-foreground">
          {silence.state === "expired"
            ? `Ended ${formatAgo(silence.endsAtMs, now)}`
            : silence.state === "pending"
              ? `Starts ${formatWhen(silence.startsAtMs, now)}, ends ${formatWhen(silence.endsAtMs, now)}`
              : `Ends ${formatWhen(silence.endsAtMs, now)}`}
          {silence.createdBy ? ` · by ${silence.createdBy}` : ""}
        </p>
      </div>
      {silence.state !== "expired" && (
        <Button variant="outline" size="sm" disabled={readOnly || expiring} loading={expiring} onClick={onExpire} data-testid={`obs-silence-expire-${silence.id}`}>
          Expire now
        </Button>
      )}
    </li>
  );
}

const OPS: [string, Pick<Matcher, "isRegex" | "isEqual">][] = [
  ["=", { isEqual: true, isRegex: false }],
  ["!=", { isEqual: false, isRegex: false }],
  ["=~", { isEqual: true, isRegex: true }],
  ["!~", { isEqual: false, isRegex: true }],
];

/** What is wrong with a silence before it is sent, in words. */
export function silenceProblems(input: CreateSilenceInput, now: number = Date.now()): string[] {
  const problems: string[] = [];
  if (input.matchers.length === 0) problems.push("Add at least one matcher, so a silence can never mute everything by accident.");
  if (input.matchers.length > 20) problems.push("A silence takes at most 20 matchers.");
  input.matchers.forEach((m, i) => {
    if (!/^[a-zA-Z_][a-zA-Z0-9_]*$/.test(m.name)) problems.push(`Matcher ${i + 1}: a label name is letters, digits and '_'.`);
    else if (m.name.startsWith("reliant_")) problems.push(`Matcher ${i + 1}: reliant_* labels are set by Reliant; the organization scope is added for you.`);
  });
  if (!input.comment.trim()) problems.push("Say why: the comment is how a teammate knows it is safe to expire.");
  if (input.endsAtMs <= now) problems.push("The end must be in the future.");
  return problems;
}

export function SilenceDialog({
  orgId,
  title,
  initial = [{ name: "alertname", value: "", isRegex: false, isEqual: true }],
  onClose,
  onCreated,
  onDenied,
}: {
  orgId: string;
  title: string;
  initial?: Matcher[];
  onClose: () => void;
  onCreated: () => void;
  onDenied: () => void;
}) {
  const [matchers, setMatchers] = useState<Matcher[]>(initial);
  const [minutes, setMinutes] = useState(DURATIONS[0][1]);
  const [comment, setComment] = useState("");
  const [problems, setProblems] = useState<string[]>([]);
  const create = useAlertingMutation(orgId, (api, org, input: CreateSilenceInput) => api.createSilence(org, input));
  const commentId = useId();
  const durationId = useId();

  const update = (i: number, patch: Partial<Matcher>) => setMatchers((prev) => prev.map((m, j) => (j === i ? { ...m, ...patch } : m)));
  const submit = () => {
    const input: CreateSilenceInput = { matchers: matchers.filter((m) => m.name.trim()), endsAtMs: Date.now() + minutes * 60_000, comment: comment.trim() };
    const found = silenceProblems(input);
    setProblems(found);
    if (found.length) return;
    create.mutate(input, {
      onSuccess: onCreated,
      onError: (error) => {
        if (alertingErrorKind(error) === "denied") {
          onDenied();
          setProblems(["Creating a silence needs the organization admin role."]);
        } else setProblems([obsErrorMessage(error) || "The silence couldn't be created."]);
      },
    });
  };

  return (
    <Modal open onClose={onClose} title={title} size="lg">
      <div className="space-y-4" data-testid="obs-silence-dialog">
        <p className="text-xs text-muted-foreground">
          Matching alerts keep being evaluated and recorded; they stop notifying until the silence ends. It applies to this organization
          only.
        </p>
        <fieldset className="space-y-2">
          <legend className="text-sm font-medium text-foreground">Matchers</legend>
          {matchers.map((m, i) => (
            <div key={i} className="grid grid-cols-[1fr_auto_1.4fr_auto] items-center gap-2" data-testid="obs-silence-matcher">
              <input aria-label={`Matcher ${i + 1} label`} value={m.name} onChange={(e) => update(i, { name: e.target.value })} placeholder="alertname" className={cn(FIELD, "font-mono")} />
              <select
                aria-label={`Matcher ${i + 1} operator`}
                value={matcherOp(m)}
                onChange={(e) => update(i, OPS.find(([op]) => op === e.target.value)![1])}
                className={cn(FIELD, "w-16")}
              >
                {OPS.map(([op]) => (
                  <option key={op} value={op}>
                    {op}
                  </option>
                ))}
              </select>
              <input aria-label={`Matcher ${i + 1} value`} value={m.value} onChange={(e) => update(i, { value: e.target.value })} placeholder="value" className={cn(FIELD, "font-mono")} />
              <button
                type="button"
                aria-label={`Remove matcher ${i + 1}`}
                onClick={() => setMatchers((prev) => prev.filter((_, j) => j !== i))}
                className="rounded p-1.5 text-muted-foreground hover:bg-muted hover:text-destructive focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </div>
          ))}
          <Button
            variant="outline"
            size="sm"
            leftIcon={<Plus className="h-3.5 w-3.5" />}
            onClick={() => setMatchers((prev) => [...prev, { name: "", value: "", isRegex: false, isEqual: true }])}
          >
            Add matcher
          </Button>
        </fieldset>
        <div className="grid gap-3 sm:grid-cols-[auto_1fr]">
          <div>
            <label htmlFor={durationId} className="mb-1 block text-xs font-medium text-muted-foreground">
              For
            </label>
            <select id={durationId} value={minutes} onChange={(e) => setMinutes(Number(e.target.value))} className={FIELD} data-testid="obs-silence-duration">
              {DURATIONS.map(([label, value]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label htmlFor={commentId} className="mb-1 block text-xs font-medium text-muted-foreground">
              Why
            </label>
            <input id={commentId} value={comment} onChange={(e) => setComment(e.target.value)} placeholder="Database maintenance until 14:00" className={FIELD} data-testid="obs-silence-comment" />
          </div>
        </div>
        {problems.length > 0 && (
          <ul role="alert" className="space-y-0.5 rounded-md border border-destructive/40 bg-background px-3 py-2 text-xs text-destructive" data-testid="obs-silence-problems">
            {problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        )}
        <div className="flex justify-end gap-2 border-t border-border/60 pt-4">
          <Button variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" size="sm" onClick={submit} loading={create.isPending} disabled={create.isPending} data-testid="obs-silence-create">
            Silence
          </Button>
        </div>
      </div>
    </Modal>
  );
}
