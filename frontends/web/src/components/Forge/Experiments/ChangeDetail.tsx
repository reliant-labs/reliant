// Copyright (c) 2025 Reliant Labs

/**
 * One change: what the release declares for it, whether its revision
 * reached the environment's apps (ChangeDelivery), who gets it, the preview
 * list, its results and its history, and the kill switch.
 *
 * ── EVERY WRITE, ONE PATH (`write`) ─────────────────────────────────────────
 *   - It names the revision the page read. A stale one is refused with
 *     `change_revision_conflict`: the page refetches the change, says what
 *     moved, and the reader decides again. Nothing is retried for them.
 *   - In a protected environment a launch, ramp up or roll-out needs a
 *     person. A browser session of the console IS a person, so there it just
 *     proceeds; any other sign-in is refused with `approval_required`, and
 *     the page offers the code flow: "Send approval code", type it, and the
 *     same write runs again with `step_up_code`.
 *   - Stop is the exception to both: one click and a confirm, never blocked,
 *     no revision (the kill switch never loses a race), never an approval.
 */

import { useCallback, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useSearch } from "@tanstack/react-router";
import { Octagon } from "lucide-react";

import AlertBanner from "@/components/forge-ui/alert_banner";
import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import EmptyState from "@/components/forge-ui/empty_state";
import Link from "@/components/forge-ui/link";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Tag from "@/components/forge-ui/tag";
import { changesServiceQueries } from "@/hooks/controlplane/changes-service-hooks_gen";
import { NO_REFETCH, rememberChange, useChange, useChangeWrites, type ChangeReply } from "@/hooks/changes-queries";
import { stopChangeRequest } from "@/services/controlPlane/changes/changesRequests";
import type { Change, ChangeApprovalCode } from "@/services/controlPlane/changes/changesTypes";
import { approvalCodeFrom, changeFrom } from "@/services/controlPlane/changes/changesWire";
import { changeErrorKind, describeChangeError } from "@/services/controlPlane/changes/errors";

import { FIELD } from "../controlStyles";
import { useObsReadOnly } from "../Observability/obsReadOnly";
import { BetaTag } from "./BetaTag";
import { ChangeHistory } from "./ChangeHistory";
import { ChangeResults } from "./ChangeResults";
import { ChangeDelivery } from "./ChangeDelivery";
import { ageOf, changedFields, deliveryPhrase, MODE_LABELS, type WhoGetsIt as Who } from "./changeModel";
import type { NewChangeEnv } from "./NewChangeDialog";
import { PreviewEditor } from "./PreviewEditor";
import { WhoGetsIt } from "./WhoGetsIt";

interface PendingApproval {
  label: string;
  write: (stepUpCode?: string) => Promise<ChangeReply>;
  /** Set once a code was sent. */
  sent: ChangeApprovalCode | null;
  /** The code the reader typed was refused. */
  refused: boolean;
}

const WHO_VALUES: readonly Who[] = ["everyone", "some", "compare"];

export function ChangeDetail({ changeId, envs, backHref }: { changeId: string; envs: NewChangeEnv[]; backHref: string }) {
  const writes = useChangeWrites();
  const queryClient = useQueryClient();
  const lock = useObsReadOnly();
  const query = useChange(changeId);
  const search = useSearch({ strict: false }) as { who?: string };
  const initialWho = WHO_VALUES.find((w) => w === search.who);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<string[] | null>(null);
  const [approval, setApproval] = useState<PendingApproval | null>(null);
  const [stopping, setStopping] = useState(false);
  const change = query.data;

  const write = useCallback(
    async (label: string, run: (stepUpCode?: string) => Promise<ChangeReply>, stepUpCode?: string): Promise<Change | null> => {
      if (!change) return null;
      setBusy(label);
      setError(null);
      try {
        const reply = await run(stepUpCode);
        rememberChange(queryClient, reply.change);
        setConflict(null);
        setApproval(null);
        return changeFrom(reply.change);
      } catch (err) {
        const kind = changeErrorKind(err);
        if (kind === "conflict") {
          setApproval(null);
          try {
            // fetchQuery writes the answer into the change's cache entry.
            const latest = await queryClient.fetchQuery(changesServiceQueries.getChange({ changeId: change.id }));
            setConflict(changedFields(change, changeFrom(latest.change)));
          } catch (refetchErr) {
            setError(describeChangeError(refetchErr));
          }
        } else if (kind === "approval-required") {
          setApproval((current) => ({ label, write: run, sent: current?.label === label ? current.sent : null, refused: !!stepUpCode }));
        } else {
          setError(describeChangeError(err));
        }
        return null;
      } finally {
        setBusy(null);
      }
    },
    [change, queryClient]
  );
  const onWrite = useCallback((label: string, run: (stepUpCode?: string) => Promise<ChangeReply>) => write(label, run), [write]);

  if (query.isLoading) {
    return (
      <Card padding="none" aria-label="Loading" aria-busy="true">
        <SkeletonLoader variant="table-row" count={4} />
      </Card>
    );
  }
  if (query.error || !change) {
    return (
      <EmptyState
        title={changeErrorKind(query.error) === "not-available" ? "Experiments aren't available here yet" : "Couldn't open this change"}
        description={query.error ? describeChangeError(query.error) : "This change no longer exists."}
      />
    );
  }

  const envName = envs.find((e) => e.id === change.environmentId)?.name ?? change.environmentId;
  const declaration = change.declaration;

  return (
    <div className="min-w-0 space-y-4" data-testid="console-experiment">
      <PageHeader
        density="compact"
        title={<span className="font-mono">{change.key}</span>}
        titleAccessory={
          <span className="inline-flex items-center gap-1.5">
            <Tag>{MODE_LABELS[change.mode]}</Tag>
            <BetaTag />
          </span>
        }
        subtitle={change.description || undefined}
        meta={
          <>
            <span>{envName}</span>
            <span>{deliveryPhrase(change)}</span>
            {change.currentRun && <span>{`Run ${change.currentRun.id}, ${ageOf(change.currentRun.startedAt)}`}</span>}
            <span className="tabular-nums">{`Revision ${change.revision}`}</span>
          </>
        }
        className="mb-0"
        controls={
          <Link href={backHref} className="text-sm text-ink-muted underline-offset-2 hover:underline">
            All changes
          </Link>
        }
        actions={[{ label: "Stop", onClick: () => setStopping(true), variant: "danger", disabled: lock.readOnly, icon: <Octagon className="h-4 w-4" aria-hidden="true" /> }]}
      />

      {!declaration && (
        <AlertBanner
          variant="info"
          title="Not declared yet"
          message={
            <>
              The release running in {envName} doesn't declare <code className="font-mono">{change.key}</code>. You can edit the draft and the preview list now; launching it waits for a release whose changes.yaml declares it.
            </>
          }
        />
      )}

      {conflict && (
        <AlertBanner
          variant="warning"
          title="Someone changed this while you were looking"
          data-testid="change-conflict"
          dismissible
          onDismiss={() => setConflict(null)}
          message={
            <>
              Nothing was saved. This is the latest version; check it and try again.
              <ul className="mt-1 list-disc pl-5">
                {conflict.map((line) => (
                  <li key={line}>{line}</li>
                ))}
              </ul>
            </>
          }
        />
      )}

      {approval && (
        <ApprovalPanel
          envName={envName}
          approval={approval}
          busy={busy}
          onSend={async () => {
            try {
              const sent = approvalCodeFrom(await writes.requestChangeApproval({ changeId: change.id }, NO_REFETCH));
              setApproval((current) => (current ? { ...current, sent, refused: false } : current));
            } catch (err) {
              setError(describeChangeError(err));
            }
          }}
          onApprove={(code) => void write(approval.label, approval.write, code)}
          onCancel={() => setApproval(null)}
        />
      )}

      {error && <AlertBanner variant="error" message={error} role="alert" dismissible onDismiss={() => setError(null)} />}

      {declaration && (
        <Card padding="md" className="space-y-2">
          <h2 className="text-section text-ink">Versions</h2>
          <p className="text-meta text-ink-muted">Declared by the release running in {envName}{declaration.type ? ` (${declaration.type})` : ""}.</p>
          <ul className="divide-y divide-border-subtle" aria-label="Versions">
            {declaration.variants.map((v) => (
              <li key={v.name} className="flex items-center gap-3 py-1.5 text-ui">
                <span className="font-mono text-ink">{v.name}</span>
                {v.name === declaration.defaultVariant && <Tag>Default</Tag>}
                <span className="ml-auto truncate font-mono text-meta text-ink-muted">{v.valueJson}</span>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <ChangeDelivery change={change} envName={envName} />
      <WhoGetsIt change={change} initialWho={initialWho} busy={busy} onWrite={onWrite} />
      <PreviewEditor change={change} busy={busy} onWrite={onWrite} />
      <ChangeResults change={change} />
      <ChangeHistory changeId={change.id} />

      <ConfirmationDialog
        open={stopping}
        title={`Stop ${change.key} in ${envName}?`}
        description="Everyone gets the default right away, previews included, and the open run ends. You can launch or roll it out again later."
        confirmLabel="Stop"
        variant="danger"
        loading={busy === "Stop"}
        onCancel={() => setStopping(false)}
        onConfirm={async () => {
          setStopping(false);
          setApproval(null);
          // No revision: stopping never loses a race with another edit.
          await write("Stop", () => writes.stopChange(stopChangeRequest(change.id), NO_REFETCH));
        }}
      />
    </div>
  );
}

function ApprovalPanel({
  envName,
  approval,
  busy,
  onSend,
  onApprove,
  onCancel,
}: {
  envName: string;
  approval: PendingApproval;
  busy: string | null;
  onSend: () => Promise<void>;
  onApprove: (code: string) => void;
  onCancel: () => void;
}) {
  const [code, setCode] = useState("");
  const [sending, setSending] = useState(false);
  const verb = approval.label.charAt(0).toLowerCase() + approval.label.slice(1);
  return (
    <Card padding="md" className="space-y-3" role="region" aria-label="Approval needed" data-testid="change-approval">
      <div>
        <h2 className="text-section text-ink">Approval needed</h2>
        <p className="text-sm text-ink-muted">
          {envName} is a protected environment, so a person has to approve this: {verb}. We'll email you a one-time code to confirm it's you.
        </p>
      </div>
      {approval.sent ? (
        <form
          className="flex flex-wrap items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            if (code.trim()) onApprove(code.trim());
          }}
        >
          <p className="basis-full text-meta text-ink-muted">
            {`We sent a code${approval.sent.sentTo ? ` to ${approval.sent.sentTo}` : ""}. It works for 10 minutes, for this one action.`}
          </p>
          <input
            value={code}
            onChange={(event) => setCode(event.target.value)}
            inputMode="numeric"
            autoComplete="one-time-code"
            placeholder="Code"
            aria-label="Approval code"
            className={`${FIELD} w-32 font-mono tracking-widest`}
            data-testid="approval-code"
          />
          <Button type="submit" variant="primary" size="sm" disabled={!code.trim() || busy !== null} loading={busy === approval.label}>
            Approve and {verb}
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
            Cancel
          </Button>
          {approval.refused && (
            <p role="alert" className="basis-full text-meta text-danger-ink">
              That code didn't work. Check it, or send a new one.
            </p>
          )}
        </form>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            size="sm"
            loading={sending}
            onClick={async () => {
              setSending(true);
              try {
                await onSend();
              } finally {
                setSending(false);
              }
            }}
            data-testid="approval-send"
          >
            Send approval code
          </Button>
          <Button variant="ghost" size="sm" onClick={onCancel}>
            Cancel
          </Button>
        </div>
      )}
    </Card>
  );
}
