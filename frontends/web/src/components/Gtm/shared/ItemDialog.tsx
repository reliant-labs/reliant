// Copyright (c) 2026 Reliant Labs

/**
 * One post, email or video, opened from the calendar, a campaign's board or
 * an idea: its format and channel, its body, where and when it goes out, its
 * files, its claim-check findings, and what a person may do with it
 * (lifecycle.ts): approve, schedule, pause or resume, and edit. A sheet on a
 * phone.
 *
 * The findings are computed live, by CheckClaims over the body (the edited
 * text while editing) against the org's facts: nothing stores them, so they
 * are never stale against a fact verified since.
 *
 * Nothing here publishes or sends.
 *   - On a MANUAL channel a person posts by hand, then records where it went
 *     out and marks it PUBLISHED in one write (published_url).
 *   - An email (EMAIL or COLD_OUTREACH) shows its recipients and subject
 *     read-only and goes out only through the agent, behind an approval card
 *     in the chat (EmailSendNote).
 *   - A HELD email (a send started, its outcome unknown) is frozen until a
 *     person resolves it: it went out (its Gmail id), or it did not (the hold
 *     is released) — ResolveItemSend.
 * A SENT item is read-only.
 */

import { Code } from "@connectrpc/connect";
import { AlertTriangle, CalendarCheck, CheckCircle2, ExternalLink, Hand, Pause, Pencil, Play } from "lucide-react";
import { useEffect, useState } from "react";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Link from "@/components/forge-ui/link";
import Modal from "@/components/forge-ui/modal";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Tag from "@/components/forge-ui/tag";
import Textarea from "@/components/forge-ui/textarea";
import { GtmChannelMode, GtmChannelStatus, GtmItemFormat, GtmItemStatus, type GtmChannel, type GtmClaimFinding, type GtmItem } from "@/gen/reliant/v1/gtm_pb";
import { useCheckClaims, useGetIdea, useListCampaigns, useListChannels, useResolveItemSend, useUpdateItem } from "@/hooks/gtm-service-hooks_gen";
import { gtmIdeaPath } from "@/lib/paths";

import { AssetPanel } from "./Assets";
import { useGtmOrgId } from "./GtmShell";
import { canApprove, canEdit, canMarkPublished, canPause, canResume, canSchedule, editReturnsToReview, isEmailItem, isHeld, postsByHand } from "./lifecycle";
import { EmailSendNote, PersonActionButton, PersonActionDialog, PersonOnlyTag } from "./PersonAction";
import { Fields, formatAndChannel, formatLabel, formatWhen, ITEM_FORMATS, ItemStatusBadge, OutboundLink } from "./ui";
import { useGtmWriteError } from "./writes";

export function ItemDialog({ item, onClose }: { item: GtmItem | null; onClose: () => void }) {
  if (!item) return null;
  return <OpenItemDialog key={item.id} item={item} onClose={onClose} />;
}

/** The org's channels, for an item's channel name and mode (one cached read shared by every GTM page). */
export function useGtmChannels(): GtmChannel[] {
  const orgId = useGtmOrgId();
  const q = useListChannels({ orgId }, { enabled: !!orgId, staleTime: 60_000 });
  return q.data?.channels ?? [];
}

const SELECT =
  "h-9 w-full rounded-md border border-border-strong bg-surface px-2.5 text-sm text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

function OpenItemDialog({ item, onClose }: { item: GtmItem; onClose: () => void }) {
  const orgId = useGtmOrgId();
  const campaigns = useListCampaigns({ orgId, pageSize: 200 }, { enabled: !!orgId });
  const idea = useGetIdea({ orgId, id: item.ideaId }, { enabled: !!orgId && item.ideaId !== "" });
  const channels = useGtmChannels();
  const channel = channels.find((c) => c.id === item.channelId);
  const campaign = campaigns.data?.campaigns.find((c) => c.id === item.campaignId);
  const isEmail = isEmailItem(item);
  const [editing, setEditing] = useState(false);
  // The body being edited, for the live claim check; null when not editing.
  const [draftBody, setDraftBody] = useState<string | null>(null);
  const checked = useDebounced(editing && draftBody !== null ? draftBody : item.bodyMd, 600);
  const stopEditing = () => {
    setEditing(false);
    setDraftBody(null);
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
          {isHeld(item) && <Tag>Send in doubt</Tag>}
          <span>{formatAndChannel(item.format, channel)}</span>
        </span>
      }
    >
      <div className="space-y-5 text-sm" data-testid="gtm-item-dialog">
        {isHeld(item) && <HeldSend item={item} />}

        <Fields
          columns={2}
          fields={[
            { label: "Format", value: formatLabel(item.format) },
            { label: "Channel", value: channel ? <ChannelValue channel={channel} /> : item.channelId ? "…" : "None yet" },
            { label: "Goes out", value: formatWhen(item.scheduledAt) },
            { label: "Campaign", value: campaign?.name ?? (item.campaignId ? "…" : "None") },
            ...(isEmail ? [{ label: "To", value: item.emailTo.length ? item.emailTo.join(", ") : "No recipients yet" }] : []),
            ...(isEmail ? [{ label: "Subject", value: item.emailSubject || "No subject yet" }] : []),
            ...(item.sentBy ? [{ label: "Sent by", value: item.sentBy }] : []),
            ...(item.publishedUrl
              ? [
                  {
                    label: "Went out at",
                    value: (
                      <OutboundLink url={item.publishedUrl} className="inline-flex items-center gap-1 break-all underline underline-offset-2">
                        {item.publishedUrl.replace(/^https?:\/\//, "")}
                        <ExternalLink className="size-3 shrink-0" aria-hidden="true" />
                      </OutboundLink>
                    ),
                  },
                ]
              : []),
            ...(idea.data?.idea
              ? [{ label: "From idea", value: <Link href={gtmIdeaPath(idea.data.idea.id)} className="underline underline-offset-2">{idea.data.idea.title}</Link> }]
              : []),
          ]}
        />

        {postsByHand(item, channel) && channel && <ManualPostStep item={item} channel={channel} />}

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
            // Keyed on the revision: when the item moves under the editor (a
            // conflict re-reads it), the fields start again from the current
            // version, so a second Save cannot overwrite what changed.
            <ItemEditor key={`${item.id}@${item.revision}`} item={item} channels={channels} onBodyChange={setDraftBody} onDone={stopEditing} />
          ) : (
            <div className="max-h-72 overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-surface-sunken p-3 text-sm text-ink">
              {item.bodyMd || "Empty."}
            </div>
          )}
        </section>

        <AssetPanel owner={{ itemId: item.id }} />

        <ClaimCheck orgId={orgId} text={checked} />

        {isEmail && item.status !== GtmItemStatus.SENT && <EmailSendNote />}

        {!editing && <ItemActions item={item} />}
      </div>
    </Modal>
  );
}

/**
 * The edit form, on one revision of the item: it sends only the fields that
 * changed, against that revision. The parent remounts it when the revision
 * moves.
 */
function ItemEditor({
  item,
  channels,
  onBodyChange,
  onDone,
}: {
  item: GtmItem;
  channels: GtmChannel[];
  onBodyChange: (body: string) => void;
  onDone: () => void;
}) {
  const orgId = useGtmOrgId();
  const [title, setTitle] = useState(item.title);
  const [body, setBody] = useState(item.bodyMd);
  const [format, setFormat] = useState<GtmItemFormat>(item.format);
  const [channelId, setChannelId] = useState(item.channelId);
  const onError = useGtmWriteError();
  const save = useUpdateItem({ onSuccess: onDone, onError });
  useEffect(() => onBodyChange(body), [body, onBodyChange]);

  return (
    <div className="space-y-3" data-testid="gtm-item-edit">
      {editReturnsToReview(item) && (
        <p className="rounded-lg border border-border bg-surface-muted px-3 py-2 text-ink-muted" data-testid="gtm-item-edit-review-note">
          This item is approved. Saving an edit sends it back to review: it needs approving again.
        </p>
      )}
      <Input aria-label="Title" value={title} onChange={(e) => setTitle(e.target.value)} />
      <div className="grid gap-3 sm:grid-cols-2">
        <select aria-label="Format" className={SELECT} value={format} onChange={(e) => setFormat(Number(e.target.value) as GtmItemFormat)}>
          {ITEM_FORMATS.map((f) => (
            <option key={f} value={f}>
              {formatLabel(f)}
            </option>
          ))}
        </select>
        <select aria-label="Channel" className={SELECT} value={channelId} onChange={(e) => setChannelId(e.target.value)}>
          <option value="">No channel yet</option>
          {pickableChannels(channels, item.channelId).map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      </div>
      <Textarea aria-label="Body" value={body} onChange={(e) => setBody(e.target.value)} rows={10} resize="vertical" />
      <div className="flex justify-end gap-2">
        <Button variant="secondary" size="sm" onClick={onDone} disabled={save.isPending}>
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
              ...(format !== item.format ? { format } : {}),
              ...(channelId !== item.channelId ? { channelId } : {}),
            })
          }
        >
          Save
        </Button>
      </div>
    </div>
  );
}

/** The channels an item may move to: none archived, except the one it is already on. */
export function pickableChannels(channels: GtmChannel[], currentId: string): GtmChannel[] {
  return channels.filter((c) => c.status !== GtmChannelStatus.ARCHIVED || c.id === currentId);
}

function ChannelValue({ channel }: { channel: GtmChannel }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      {channel.name}
      <Tag>{channel.mode === GtmChannelMode.MANUAL ? "Manual" : "Integrated"}</Tag>
    </span>
  );
}

/**
 * The manual channel's step: post it by hand, then record where it went out.
 * Before approval it only says what will happen; once approved, a person
 * pastes the URL and marks it PUBLISHED in one UpdateItem.
 */
function ManualPostStep({ item, channel }: { item: GtmItem; channel: GtmChannel }) {
  const orgId = useGtmOrgId();
  const [url, setUrl] = useState("");
  const onError = useGtmWriteError();
  const publish = useUpdateItem({ onError });
  const valid = /^https?:\/\/\S+$/.test(url.trim());
  return (
    <section aria-labelledby="gtm-manual-step" className="rounded-lg border border-border bg-surface-muted p-4" data-testid="gtm-manual-step">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <Hand className="size-4 text-ink-muted" aria-hidden="true" />
        <h3 id="gtm-manual-step" className="text-sm font-medium text-ink">
          Post by hand on {channel.name}
        </h3>
        <PersonOnlyTag />
      </div>
      {channel.manualInstructionsMd && <p className="whitespace-pre-wrap text-ink-muted">{channel.manualInstructionsMd}</p>}
      {canMarkPublished(item, channel) ? (
        <form
          className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-center"
          onSubmit={(e) => {
            e.preventDefault();
            if (valid) publish.mutate({ orgId, id: item.id, expectedRevision: item.revision, status: GtmItemStatus.PUBLISHED, publishedUrl: url.trim() });
          }}
        >
          <label htmlFor="gtm-published-url" className="sr-only">
            Where it went out
          </label>
          <Input
            id="gtm-published-url"
            type="url"
            inputMode="url"
            placeholder={`https://… (the ${channel.name} post)`}
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            className="sm:flex-1"
          />
          <Button type="submit" variant="primary" size="sm" disabled={!valid} loading={publish.isPending}>
            Mark published
          </Button>
        </form>
      ) : (
        <p className="mt-2 text-ink-muted">Once it is approved, post it and paste the URL here to mark it published.</p>
      )}
    </section>
  );
}

/**
 * A HELD email: its send started and its outcome is unknown. A person checks
 * the sent mail in Gmail and resolves it: it went out (paste its Gmail
 * message id: the item becomes SENT) or it did not (the hold is released and
 * it stays APPROVED, to be sent again through the agent). A release the
 * service refuses because the send may still be in flight says so: wait a
 * few minutes and check again.
 */
function HeldSend({ item }: { item: GtmItem }) {
  const orgId = useGtmOrgId();
  const [choice, setChoice] = useState<"sent" | "not-sent" | null>(null);
  const [gmailId, setGmailId] = useState("");
  const [refusal, setRefusal] = useState<string | null>(null);
  const onError = useGtmWriteError();
  const resolve = useResolveItemSend({
    onSuccess: () => setChoice(null),
    onError: (error) => {
      // A refusal (the send may still be in flight, the item moved) stays in the dialog, in the server's words.
      const message = error.code === Code.FailedPrecondition ? error.rawMessage : null;
      if (message) setRefusal(message.charAt(0).toUpperCase() + message.slice(1));
      else {
        setChoice(null);
        onError(error);
      }
    },
  });
  const close = () => {
    setChoice(null);
    setRefusal(null);
    setGmailId("");
  };
  const confirm = () =>
    resolve.mutate({ orgId, id: item.id, approvalHash: item.approvalHash, ...(choice === "sent" ? { gmailMessageId: gmailId.trim() } : {}) });

  return (
    <section aria-labelledby="gtm-held" className="rounded-lg border border-warning/40 bg-warning-surface p-4" data-testid="gtm-held-send">
      <div className="mb-1 flex items-center gap-2">
        <AlertTriangle className="size-4 text-warning-ink" aria-hidden="true" />
        <h3 id="gtm-held" className="text-sm font-medium text-ink">
          Send in doubt
        </h3>
        <PersonOnlyTag />
      </div>
      <p className="text-ink-muted">
        A send of this email started and was not recorded. Check the sent mail in Gmail, then say what happened. Until then the email cannot be
        changed or sent again.
      </p>
      <div className="mt-3 flex flex-wrap gap-2">
        <Button variant="outline" size="sm" onClick={() => setChoice("sent")}>
          It went out
        </Button>
        <Button variant="outline" size="sm" onClick={() => setChoice("not-sent")}>
          It did not go out
        </Button>
      </div>
      <Modal
        open={choice !== null}
        onClose={close}
        size="sm"
        sheetOnMobile
        title={choice === "sent" ? "It went out" : "It did not go out"}
        description={item.title}
        footer={
          <div className="flex w-full justify-end gap-2">
            <Button variant="secondary" size="sm" onClick={close} disabled={resolve.isPending}>
              Cancel
            </Button>
            <Button variant="primary" size="sm" onClick={confirm} loading={resolve.isPending} disabled={choice === "sent" && gmailId.trim() === ""}>
              {choice === "sent" ? "Record as sent" : "Release the hold"}
            </Button>
          </div>
        }
      >
        <div className="space-y-3 text-sm" data-testid="gtm-resolve-send">
          {choice === "sent" ? (
            <>
              <p className="text-ink">You found the email in Gmail&apos;s sent mail. Its message id records it as sent, by you.</p>
              <label htmlFor="gtm-gmail-id" className="block font-medium text-ink">
                Gmail message id
              </label>
              <Input id="gtm-gmail-id" value={gmailId} onChange={(e) => setGmailId(e.target.value)} placeholder="18c2…" />
            </>
          ) : (
            <p className="text-ink">
              You checked Gmail&apos;s sent mail and it is not there. The hold is released and the email stays approved, to be sent again through the
              agent.
            </p>
          )}
          {refusal && (
            <p className="rounded-md border border-border bg-surface-muted px-3 py-2 text-ink" role="alert" data-testid="gtm-resolve-refusal">
              {refusal}
            </p>
          )}
        </div>
      </Modal>
    </section>
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
  if (isHeld(item)) return null;
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
      {approved && !isEmailItem(item) && <p className="text-ink-muted">Approved. Posting is a separate step; nothing here publishes.</p>}
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
