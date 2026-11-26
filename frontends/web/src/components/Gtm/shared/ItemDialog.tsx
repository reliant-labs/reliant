// Copyright (c) 2026 Reliant Labs

/**
 * One post, email or launch, opened from the calendar, a campaign's board or
 * an idea: its body, where and when it goes out, its claim-check findings,
 * and Approve. A sheet on a phone.
 *
 * The findings are computed live, by CheckClaims over the body against the
 * org's facts, each time the item is opened: nothing stores them, so they are
 * never stale against a fact verified since.
 *
 * An email shows its recipients and subject read-only. It has no send
 * button: sending is the agent's, behind an approval card (EmailSendNote).
 */

import { AlertTriangle, CheckCircle2 } from "lucide-react";
import { useState } from "react";

import Link from "@/components/forge-ui/link";
import Modal from "@/components/forge-ui/modal";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { GtmItemKind, GtmItemStatus, type GtmClaimFinding, type GtmItem } from "@/gen/reliant/v1/gtm_pb";
import { useCheckClaims, useGetIdea, useListCampaigns, useUpdateItem } from "@/hooks/gtm-service-hooks_gen";
import { gtmIdeaPath } from "@/lib/paths";

import { useGtmOrgId } from "./GtmShell";
import { EmailSendNote, PersonActionButton, PersonActionDialog } from "./PersonAction";
import { Fields, formatWhen, isItemOut, ItemStatusBadge, kindAndChannel } from "./ui";
import { useGtmWriteError } from "./writes";

/** Statuses a person may approve from: what is still being drafted or reviewed. */
const APPROVABLE: readonly GtmItemStatus[] = [GtmItemStatus.DRAFT, GtmItemStatus.REVIEW];

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
  const out = isItemOut(item.status);

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
          <h3 id="gtm-item-body" className="mb-2 text-sm font-medium text-ink">
            {isEmail ? "Email body" : "Draft"}
          </h3>
          <div className="max-h-72 overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-surface-sunken p-3 text-sm text-ink">
            {item.bodyMd || "Empty."}
          </div>
        </section>

        <ClaimCheck orgId={orgId} text={item.bodyMd} />

        {isEmail && !out && <EmailSendNote />}

        {!out && (
          <section aria-label="Actions" className="flex flex-wrap items-center gap-3 border-t border-border pt-4">
            {APPROVABLE.includes(item.status) ? (
              <ApproveItem item={item} />
            ) : (
              <p className="text-ink-muted">
                {item.status === GtmItemStatus.APPROVED || item.status === GtmItemStatus.SCHEDULED
                  ? "Approved. Posting is a separate step; nothing here publishes."
                  : "Nothing to approve in this state."}
              </p>
            )}
          </section>
        )}
      </div>
    </Modal>
  );
}

/** Approve: a person's call (UpdateItem status APPROVED, on the revision on screen). */
function ApproveItem({ item }: { item: GtmItem }) {
  const orgId = useGtmOrgId();
  const [open, setOpen] = useState(false);
  const onError = useGtmWriteError();
  const update = useUpdateItem({
    onSuccess: () => setOpen(false),
    onError: (error) => {
      setOpen(false);
      onError(error);
    },
  });
  return (
    <>
      <PersonActionButton action="approve" subject={item.title} onClick={() => setOpen(true)} />
      <PersonActionDialog
        open={open}
        onClose={() => setOpen(false)}
        action="approve"
        subject={item.title}
        busy={update.isPending}
        onConfirm={() => update.mutate({ orgId, id: item.id, expectedRevision: item.revision, status: GtmItemStatus.APPROVED })}
      />
    </>
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
