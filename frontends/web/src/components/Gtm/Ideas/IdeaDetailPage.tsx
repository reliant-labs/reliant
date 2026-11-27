// Copyright (c) 2026 Reliant Labs

/**
 * One idea: why it was proposed, its evidence, its draft, and what has come
 * of it (the posts and emails made from it). A person accepts or rejects it
 * here, and may edit the draft. Every write names the revision on screen
 * (expected_revision); a conflict says so and shows the latest
 * (shared/writes.ts).
 */

import { Code } from "@connectrpc/connect";
import { useParams } from "@tanstack/react-router";
import { Check, Pencil, X } from "lucide-react";
import { useState } from "react";

import Button from "@/components/forge-ui/button";
import Card, { CardBody, CardHeader } from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import Link from "@/components/forge-ui/link";
import Modal from "@/components/forge-ui/modal";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Textarea from "@/components/forge-ui/textarea";
import { GtmIdeaStatus, type GtmIdea, type GtmItem } from "@/gen/reliant/v1/gtm_pb";
import { useGetIdea, useListCampaigns, useListItems, useSetIdeaStatus, useUpdateIdea } from "@/hooks/gtm-service-hooks_gen";
import { chatPath, gtmPath } from "@/lib/paths";

import { GtmShell, useGtmOrgId } from "../shared/GtmShell";
import { ItemDialog } from "../shared/ItemDialog";
import { PersonOnlyTag } from "../shared/PersonAction";
import { useGtmChannels } from "../shared/ItemDialog";
import { Fields, formatAndChannel, formatDay, formatWhen, IDEA_KIND, IdeaStatusBadge, ItemStatusBadge, SourceTag } from "../shared/ui";
import { useGtmWriteError } from "../shared/writes";
import { joinIdeaBody, splitIdeaBody } from "./ideaBody";

export function IdeaDetailPage() {
  return (
    <GtmShell page="ideas">
      <IdeaDetailView />
    </GtmShell>
  );
}

export function IdeaDetailView() {
  const orgId = useGtmOrgId();
  const { ideaId = "" } = useParams({ strict: false }) as { ideaId?: string };
  const ideaQ = useGetIdea({ orgId, id: ideaId }, { enabled: !!orgId && ideaId !== "" });
  const campaignsQ = useListCampaigns({ orgId, pageSize: 200 }, { enabled: !!orgId });
  const itemsQ = useListItems({ orgId, pageSize: 500 }, { enabled: !!orgId });
  const [openItemId, setOpenItemId] = useState<string | null>(null);

  if (ideaId !== "" && ideaQ.isPending) return <SkeletonLoader variant="card" count={2} />;
  const idea = ideaQ.data?.idea;
  if (ideaQ.isError && ideaQ.error.code !== Code.NotFound) {
    return <EmptyState title="Couldn't load this idea" description="Something went wrong reading it." actionLabel="Try again" onAction={() => void ideaQ.refetch()} />;
  }
  if (!idea) return <EmptyState title="Idea not found" description="It may have been removed, or it belongs to another org." />;
  const campaign = campaignsQ.data?.campaigns.find((c) => c.id === idea.campaignId);
  const items = itemsQ.data?.items ?? [];
  const made = items.filter((i) => i.ideaId === idea.id);
  const { why } = splitIdeaBody(idea.bodyMd);

  return (
    <>
      <PageHeader
        breadcrumbs={[{ label: "Ideas", href: gtmPath("ideas") }, { label: idea.title }]}
        title={idea.title}
        titleAccessory={<IdeaStatusBadge status={idea.status} />}
        meta={
          <>
            <SourceTag source={idea.source} />
            {idea.sourceChatId ? (
              <Link href={chatPath(idea.sourceChatId)} className="underline underline-offset-2">
                From a chat
              </Link>
            ) : null}
            {idea.createdBy ? <span>{idea.createdBy}</span> : null}
            {IDEA_KIND[idea.kind] ? <span>{IDEA_KIND[idea.kind]}</span> : null}
            <span>Proposed {formatDay(idea.createdAt)}</span>
          </>
        }
        className=""
      />
      <IdeaDecision idea={idea} />

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div className="min-w-0 space-y-6">
          <Card>
            <CardHeader title="Why" />
            <CardBody>
              <p className="whitespace-pre-wrap text-sm text-ink">{why || "No rationale given."}</p>
            </CardBody>
          </Card>
          <DraftCard key={`${idea.id}@${idea.revision}`} idea={idea} />
        </div>
        <div className="space-y-6">
          <Card>
            <CardHeader title="Evidence" />
            <CardBody>
              {idea.evidence.length === 0 ? (
                <p className="text-sm text-ink-muted">None attached.</p>
              ) : (
                <ul className="space-y-3">
                  {idea.evidence.map((e, i) => (
                    <li key={`${e.title}-${i}`}>
                      <span className="block whitespace-pre-wrap text-sm font-semibold text-ink">{e.summaryMd || "—"}</span>
                      <span className="block text-xs text-ink-muted">
                        {e.url ? (
                          <Link href={e.url} className="underline underline-offset-2">
                            {e.title}
                          </Link>
                        ) : (
                          e.title
                        )}
                        {e.observedAt ? ` · ${formatDay(e.observedAt)}` : ""}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </CardBody>
          </Card>
          <Card>
            <CardHeader title="Made from this idea" />
            <CardBody>
              <Fields fields={[{ label: "Campaign", value: campaign?.name ?? (idea.campaignId ? "…" : "None") }]} />
              {itemsQ.isPending ? (
                <SkeletonLoader variant="text" count={2} />
              ) : made.length === 0 ? (
                <p className="mt-2 text-sm text-ink-muted">No posts or emails yet.</p>
              ) : (
                <ul className="mt-2 divide-y divide-border">
                  {made.map((item) => (
                    <MadeItem key={item.id} item={item} onOpen={() => setOpenItemId(item.id)} />
                  ))}
                </ul>
              )}
            </CardBody>
          </Card>
        </div>
      </div>
      <ItemDialog item={items.find((i) => i.id === openItemId) ?? null} onClose={() => setOpenItemId(null)} />
    </>
  );
}

function MadeItem({ item, onOpen }: { item: GtmItem; onOpen: () => void }) {
  const channel = useGtmChannels().find((c) => c.id === item.channelId);
  return (
    <li>
      <button
        type="button"
        onClick={onOpen}
        className="flex w-full flex-col gap-1 py-2.5 text-left hover:bg-ink/5 focus-visible:outline-2 focus-visible:outline-accent"
      >
        <span className="text-sm font-medium text-ink">{item.title}</span>
        <span className="flex flex-wrap items-center gap-2 text-xs text-ink-muted">
          <ItemStatusBadge status={item.status} />
          {formatAndChannel(item.format, channel)} · {formatWhen(item.scheduledAt)}
        </span>
      </button>
    </li>
  );
}

/** Accept or reject: a person's call (SetIdeaStatus), so it is here and never in an agent's tools. */
function IdeaDecision({ idea }: { idea: GtmIdea }) {
  const orgId = useGtmOrgId();
  const [rejecting, setRejecting] = useState(false);
  const onError = useGtmWriteError();
  const setStatus = useSetIdeaStatus({ onSuccess: () => setRejecting(false), onError: (e) => (setRejecting(false), onError(e)) });
  const decide = (status: GtmIdeaStatus) => setStatus.mutate({ orgId, id: idea.id, expectedRevision: idea.revision, status });
  const pending = setStatus.isPending ? setStatus.variables?.status : undefined;

  if (idea.status !== GtmIdeaStatus.PROPOSED) {
    return (
      <div className="flex flex-wrap items-center gap-3 text-sm text-ink-muted">
        <span>{idea.status === GtmIdeaStatus.ACCEPTED ? "Accepted." : idea.status === GtmIdeaStatus.DONE ? "Done." : "Rejected."}</span>
        <Button variant="ghost" size="sm" onClick={() => decide(GtmIdeaStatus.PROPOSED)} loading={pending === GtmIdeaStatus.PROPOSED}>
          Reopen
        </Button>
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Decide">
      <Button variant="primary" size="sm" leftIcon={<Check />} onClick={() => decide(GtmIdeaStatus.ACCEPTED)} loading={pending === GtmIdeaStatus.ACCEPTED} disabled={setStatus.isPending}>
        Accept
      </Button>
      <Button variant="outline" size="sm" leftIcon={<X />} onClick={() => setRejecting(true)} disabled={setStatus.isPending}>
        Reject
      </Button>
      <PersonOnlyTag />
      <Modal
        open={rejecting}
        onClose={() => setRejecting(false)}
        size="sm"
        title="Reject this idea"
        description={idea.title}
        footer={
          <div className="flex justify-end gap-2">
            <Button variant="secondary" size="sm" onClick={() => setRejecting(false)} disabled={setStatus.isPending}>
              Cancel
            </Button>
            <Button variant="danger" size="sm" onClick={() => decide(GtmIdeaStatus.REJECTED)} loading={pending === GtmIdeaStatus.REJECTED}>
              Reject
            </Button>
          </div>
        }
      >
        <p className="text-sm text-ink">The idea stays in the list, under Rejected, and the agent sees it was turned down. You can reopen it later.</p>
      </Modal>
    </div>
  );
}

/** The draft the idea carries (its body after the first paragraph); editing writes the body back whole. */
function DraftCard({ idea }: { idea: GtmIdea }) {
  const orgId = useGtmOrgId();
  const body = splitIdeaBody(idea.bodyMd);
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(body.draft);
  const onError = useGtmWriteError();
  const update = useUpdateIdea({ onSuccess: () => setEditing(false), onError });
  const save = () => update.mutate({ orgId, id: idea.id, expectedRevision: idea.revision, bodyMd: joinIdeaBody({ why: body.why, draft: text }) });
  return (
    <Card>
      <CardHeader
        title="Draft"
        actions={
          editing ? (
            <div className="flex gap-2">
              <Button variant="secondary" size="sm" onClick={() => { setText(body.draft); setEditing(false); }} disabled={update.isPending}>
                Cancel
              </Button>
              <Button variant="primary" size="sm" onClick={save} loading={update.isPending}>
                Save draft
              </Button>
            </div>
          ) : (
            <Button variant="ghost" size="sm" leftIcon={<Pencil />} onClick={() => setEditing(true)}>
              Edit
            </Button>
          )
        }
      />
      <CardBody>
        {editing ? (
          <Textarea aria-label="Draft" value={text} onChange={(e) => setText(e.target.value)} rows={14} resize="vertical" className="font-mono" />
        ) : (
          <div className="whitespace-pre-wrap text-sm leading-relaxed text-ink" data-testid="gtm-idea-draft">
            {body.draft || <span className="text-ink-muted">No draft yet.</span>}
          </div>
        )}
      </CardBody>
    </Card>
  );
}
