// Copyright (c) 2026 Reliant Labs

/**
 * One post, email or launch, opened from the calendar, a campaign's board or
 * an idea: its body, where and when it goes out, its claim-check findings,
 * and what a person may do with it (lifecycle.ts): approve, schedule, pause
 * or resume, and edit. A sheet on a phone.
 *
 * The findings are computed live, by CheckClaims over the body (the edited
 * text while editing) against the org's facts: nothing stores them, so they
 * are never stale against a fact verified since.
 *
 * An email shows its recipients and subject read-only. It has no send
 * button: sending is the agent's, behind an approval card (EmailSendNote).
 * A SENT item is read-only.
 */

import { AlertTriangle, CalendarCheck, CheckCircle2, Pause, Pencil, Play } from "lucide-react";
import { useEffect, useState } from "react";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Link from "@/components/forge-ui/link";
import Modal from "@/components/forge-ui/modal";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Textarea from "@/components/forge-ui/textarea";
import { GtmItemKind, GtmItemStatus, type GtmClaimFinding, type GtmItem } from "@/gen/reliant/v1/gtm_pb";
import { useCheckClaims, useGetIdea, useListCampaigns, useUpdateItem } from "@/hooks/gtm-service-hooks_gen";
import { gtmIdeaPath } from "@/lib/paths";

import { useGtmOrgId } from "./GtmShell";
import { canApprove, canEdit, canPause, canResume, canSchedule, editReturnsToReview } from "./lifecycle";
import { EmailSendNote, PersonActionButton, PersonActionDialog } from "./PersonAction";
import { Fields, formatWhen, ItemStatusBadge, kindAndChannel } from "./ui";
import { useGtmWriteError } from "./writes";

export function ItemDialog({ item, onClose }: { item: GtmItem | null; onClose: () => void }) {
  if (!item) return null;
  return <OpenItemDialog key={item.id} item={item} onClose={onClose} />;
}

function OpenItemDialog({ item, onClose }: { item: GtmItem; onClose: () => void }) {
  const orgId = useGtmOrgId();
  const campaigns = useListCampaigns({ orgId, pageSize: 200 }, { enabled: !!orgId });
  const idea = useGetIdea({ orgId, id: item.ideaId }, { enabled: !!orgId && item.ideaId !== "" });
  const campaign = campaigns.data?.campaigns.find((c) => c.id === item.campaignId);
  const isEmail = item.kind === GtmItemKind.EMAIL;
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState(item.title);
  const [body, setBody] = useState(item.bodyMd);
  const checked = useDebounced(editing ? body : item.bodyMd, 600);
  const onError = useGtmWriteError();
  const save = useUpdateItem({ onSuccess: () => setEditing(false), onError });
  const cancelEdit = () => {
    setTitle(item.title);
    setBody(item.bodyMd);
    setEditing(false);
  };

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      sheetOnMobile
      title={item.title}
      description={
        <span className="inline-flex flex-wrap items-center gap-2">
          <ItemStatusBadge status={item.status} />
          <span>{kindAndChannel(item.kind, item.channel)}</span>
        </span>
      }
    >
      <div className="space-y-5 text-sm" data-testid="gtm-item-dialog">
        <Fields
          columns={2}
          fields={[
            { label: "Goes out", value: formatWhen(item.scheduledAt) },
            { label: "Campaign", value: campaign?.name ?? (item.campaignId ? "…" : "None") },
            ...(isEmail ? [{ label: "To", value: item.emailTo.length ? item.emailTo.join(", ") : "No recipients yet" }] : []),
            ...(isEmail ? [{ label: "Subject", value: item.emailSubject || "No subject yet" }] : []),
            ...(item.sentBy ? [{ label: "Sent by", value: item.sentBy }] : []),
            ...(idea.data?.idea
              ? [{ label: "From idea", value: <Link href={gtmIdeaPath(idea.data.idea.id)} className="underline underline-offset-2">{idea.data.idea.title}</Link> }]
              : []),
          ]}
        />

        <section aria-labelledby="gtm-item-body">
          <div className="mb-2 flex items-center justify-between gap-2">
            <h3 id="gtm-item-body" className="text-sm font-medium text-ink">
              {isEmail ? "Email body" : "Draft"}
            </h3>
            {canEdit(item) && !editing && (
              <Button variant="ghost" size="xs" leftIcon={<Pencil />} onClick={() => setEditing(true)}>
                Edit
              </Button>
            )}
          </div>
          {editing ? (
            <div className="space-y-3" data-testid="gtm-item-edit">
              {editReturnsToReview(item) && (
                <p className="rounded-lg border border-border bg-surface-muted px-3 py-2 text-ink-muted" data-testid="gtm-item-edit-review-note">
                  This item is approved. Saving an edit sends it back to review: it needs approving again.
                </p>
              )}
              <Input aria-label="Title" value={title} onChange={(e) => setTitle(e.target.value)} />
              <Textarea aria-label="Body" value={body} onChange={(e) => setBody(e.target.value)} rows={10} resize="vertical" />
              <div className="flex justify-end gap-2">
                <Button variant="secondary" size="sm" onClick={cancelEdit} disabled={save.isPending}>
                  Cancel
                </Button>
                <Button
                  variant="primary"
                  size="sm"
                  loading={save.isPending}
                  disabled={title.trim() === ""}
                  onClick={() =>
                    save.mutate({
                      orgId,
                      id: item.id,
                      expectedRevision: item.revision,
                      ...(title !== item.title ? { title: title.trim() } : {}),
                      ...(body !== item.bodyMd ? { bodyMd: body } : {}),
                    })
                  }
                >
                  Save
                </Button>
              </div>
            </div>
          ) : (
            <div className="max-h-72 overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-surface-sunken p-3 text-sm text-ink">
              {item.bodyMd || "Empty."}
            </div>
          )}
        </section>

        <ClaimCheck orgId={orgId} text={checked} />

        {isEmail && item.status !== GtmItemStatus.SENT && <EmailSendNote />}

        {!editing && <ItemActions item={item} />}
      </div>
    </Modal>
  );
}

/** `value`, once it has stopped changing for `ms`: the claim check runs on a pause in typing, not on every key. */
function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return settled;
}

/** What a person may do with the item now (lifecycle.ts), each on the revision on screen. */
function ItemActions({ item }: { item: GtmItem }) {
  const orgId = useGtmOrgId();
  const [approving, setApproving] = useState(false);
  const onError = useGtmWriteError();
  const update = useUpdateItem({ onSuccess: () => setApproving(false), onError: (e) => (setApproving(false), onError(e)) });
  const moveTo = (status: GtmItemStatus) => update.mutate({ orgId, id: item.id, expectedRevision: item.revision, status });
  const pending = update.isPending ? update.variables?.status : undefined;

  if (item.status === GtmItemStatus.SENT) {
    return <p className="border-t border-border pt-4 text-ink-muted">Sent. A sent email is read-only.</p>;
  }
  const approved = item.status === GtmItemStatus.APPROVED || item.status === GtmItemStatus.SCHEDULED;
  return (
    <section aria-label="Actions" className="flex flex-wrap items-center gap-3 border-t border-border pt-4">
      {canApprove(item) && (
        <>
          <PersonActionButton action="approve" subject={item.title} onClick={() => setApproving(true)} />
          <PersonActionDialog
            open={approving}
            onClose={() => setApproving(false)}
            action="approve"
            subject={item.title}
            busy={pending === GtmItemStatus.APPROVED}
            onConfirm={() => moveTo(GtmItemStatus.APPROVED)}
          />
        </>
      )}
      {canSchedule(item) && (
        <Button variant="outline" size="sm" leftIcon={<CalendarCheck />} onClick={() => moveTo(GtmItemStatus.SCHEDULED)} loading={pending === GtmItemStatus.SCHEDULED}>
          Mark scheduled
        </Button>
      )}
      {canResume(item) && (
        <Button variant="outline" size="sm" leftIcon={<Play />} onClick={() => moveTo(GtmItemStatus.DRAFT)} loading={pending === GtmItemStatus.DRAFT}>
          Resume as draft
        </Button>
      )}
      {canPause(item) && (
        <Button variant="ghost" size="sm" leftIcon={<Pause />} onClick={() => moveTo(GtmItemStatus.PAUSED)} loading={pending === GtmItemStatus.PAUSED}>
          Pause
        </Button>
      )}
      {approved && <p className="text-ink-muted">Approved. Posting is a separate step; nothing here publishes.</p>}
      {item.status === GtmItemStatus.PUBLISHED && <p className="text-ink-muted">Published outside GTM.</p>}
    </section>
  );
}

/** The claim check: which statements in `text` the org's verified facts support, and which block. */
export function ClaimCheck({ orgId, text }: { orgId: string; text: string }) {
  const check = useCheckClaims({ orgId, text }, { enabled: !!orgId && text.trim() !== "", staleTime: 0 });
  const findings: GtmClaimFinding[] = check.data?.findings ?? [];
  const blocking = findings.filter((f) => f.blocking).length;
  return (
    <section aria-labelledby="gtm-item-claims" data-testid="gtm-claim-check">
      <h3 id="gtm-item-claims" className="mb-2 text-sm font-medium text-ink">
        Claim check {blocking > 0 ? <span className="text-danger-ink">· {blocking} blocking</span> : null}
      </h3>
      {text.trim() === "" ? (
        <p className="text-ink-muted">Nothing to check yet.</p>
      ) : check.isPending ? (
        <SkeletonLoader variant="text" count={2} />
      ) : check.isError ? (
        <p className="text-ink-muted">The claim check could not run. Reopen this to try again.</p>
      ) : findings.length === 0 ? (
        <p className="text-ink-muted">No claims found in this copy.</p>
      ) : (
        <ul className="space-y-1.5">
          {findings.map((f, i) => (
            <li key={`${f.mention}-${i}`} className="flex items-start gap-2">
              {f.blocking ? (
                <AlertTriangle className="mt-0.5 size-4 shrink-0 text-danger-ink" aria-label="Blocking" />
              ) : (
                <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success-ink" aria-label="Supported" />
              )}
              <span className={f.blocking ? "text-ink" : "text-ink-muted"}>
                <span className="font-medium">{f.mention}</span>
                {findingNote(f) ? <span>: {findingNote(f)}</span> : null}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function findingNote(f: GtmClaimFinding): string {
  if (f.reason) return f.reason;
  if (f.claimText) return `supported by “${f.claimText}”`;
  return "";
}
