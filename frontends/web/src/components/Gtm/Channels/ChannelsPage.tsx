// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Channels: where items go out (ListChannels). INTEGRATED channels go
 * through a Reliant integration (email through Gmail) and show whether it is
 * connected for you (connection_state, resolved for the caller); MANUAL ones
 * a person posts on by hand, then records the URL on the item.
 *
 * Connecting an integration is a person's act, through Reliant's existing
 * connect flow: "Connect" opens the Connectors page, and no agent can do it
 * (scope r3.1). An agent may add MANUAL channels, which carry an "Added by
 * agent" badge (source AGENT). New channel and Edit open ChannelForm.
 */

import { Pencil, Plug, Plus } from "lucide-react";
import { useMemo, useState, type ReactNode } from "react";

import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import EmptyState from "@/components/forge-ui/empty_state";
import Mono from "@/components/forge-ui/mono";
import PageHeader from "@/components/forge-ui/page_header";
import Tag from "@/components/forge-ui/tag";
import { GtmChannelMode, GtmConnectionState, type GtmChannel } from "@/gen/reliant/v1/gtm_pb";
import { useListCampaigns, useListChannels, useListItems } from "@/hooks/gtm-service-hooks_gen";
import { connectorsPath } from "@/lib/paths";

import { PersonOnlyTag } from "../shared/PersonAction";
import { GtmShell, useGtmOrgId } from "../shared/GtmShell";
import { ChannelSourceTag, channelColor, ConnectionBadge } from "../shared/ui";
import { ChannelForm, STATUS_LABEL } from "./ChannelForm";

type ChannelRow = GtmChannel & Record<string, unknown>;

/** The most items and campaigns read to count each channel's use; past it the counts say "+". */
const USAGE_PAGE = 200;

const INTEGRATION_LABEL: Record<string, string> = { gmail: "Gmail" };
const integrationLabel = (kind: string) => INTEGRATION_LABEL[kind] ?? kind;

export function ChannelsPage() {
  return (
    <GtmShell page="channels">
      <ChannelsView />
    </GtmShell>
  );
}

/**
 * A channel's name, who added it (when given) and its key. The tag rides on
 * the name's line, which truncates the name before the tag would wrap under
 * it; the key is what moves to a second line.
 */
function ChannelName({ channel, channels, tag }: { channel: GtmChannel; channels: GtmChannel[]; tag?: ReactNode }) {
  return (
    <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
      <span className="flex min-w-0 max-w-full items-center gap-2">
        <span aria-hidden="true" className="size-2.5 shrink-0 rounded-full" style={{ background: channelColor(channel, channels) }} />
        <span className="truncate font-medium text-ink" title={channel.name}>
          {channel.name}
        </span>
        {tag && <span className="shrink-0 whitespace-nowrap">{tag}</span>}
      </span>
      <Mono value={channel.key} copy={false} className="text-xs text-ink-muted" />
    </span>
  );
}

export function ChannelsView() {
  const orgId = useGtmOrgId();
  const channelsQ = useListChannels({ orgId }, { enabled: !!orgId });
  const itemsQ = useListItems({ orgId, pageSize: USAGE_PAGE }, { enabled: !!orgId });
  const campaignsQ = useListCampaigns({ orgId, pageSize: USAGE_PAGE }, { enabled: !!orgId });
  // undefined: closed; null: a new channel; an id: editing that channel, read
  // live from the list so that after a conflict it is on the current revision.
  const [editing, setEditing] = useState<string | null | undefined>(undefined);

  const channels = useMemo(() => channelsQ.data?.channels ?? [], [channelsQ.data]);
  const editingChannel = typeof editing === "string" ? channels.find((c) => c.id === editing) : undefined;
  const usage = useMemo(() => {
    const u = new Map<string, { items: number; campaigns: number }>();
    const bump = (id: string, k: "items" | "campaigns") => {
      const v = u.get(id) ?? { items: 0, campaigns: 0 };
      v[k] += 1;
      u.set(id, v);
    };
    for (const i of itemsQ.data?.items ?? []) if (i.channelId) bump(i.channelId, "items");
    for (const c of campaignsQ.data?.campaigns ?? []) for (const id of new Set(c.plannedChannelIds)) bump(id, "campaigns");
    return u;
  }, [itemsQ.data, campaignsQ.data]);
  const moreItems = !!itemsQ.data?.nextPageToken;
  const moreCampaigns = !!campaignsQ.data?.nextPageToken;
  const usageText = (id: string) => {
    if (!itemsQ.data || !campaignsQ.data) return "";
    const u = usage.get(id) ?? { items: 0, campaigns: 0 };
    return `${u.items}${moreItems ? "+" : ""} ${u.items === 1 && !moreItems ? "item" : "items"} · ${u.campaigns}${moreCampaigns ? "+" : ""} ${u.campaigns === 1 && !moreCampaigns ? "campaign" : "campaigns"}`;
  };

  const integrated = channels.filter((c) => c.mode === GtmChannelMode.INTEGRATED) as ChannelRow[];
  const manual = channels.filter((c) => c.mode !== GtmChannelMode.INTEGRATED) as ChannelRow[];

  const editColumn: DataTableColumn<ChannelRow> = {
    key: "edit",
    header: "Edit",
    srHeader: true,
    interactive: true,
    width: "70px",
    render: (_, row) => (
      <Button variant="ghost" size="xs" leftIcon={<Pencil />} onClick={() => setEditing(row.id)} aria-haspopup="dialog" aria-label={`Edit ${row.name}`}>
        Edit
      </Button>
    ),
  };
  const usageColumn: DataTableColumn<ChannelRow> = {
    key: "usage",
    header: "Used by",
    width: "170px",
    render: (_, row) => <span className="block whitespace-normal text-ink-muted">{usageText(row.id)}</span>,
  };

  const statusColumn: DataTableColumn<ChannelRow> = {
    key: "status",
    header: "Status",
    width: "100px",
    render: (_, row) => <Tag>{STATUS_LABEL[row.status] ?? "Unknown"}</Tag>,
  };

  const integratedColumns: DataTableColumn<ChannelRow>[] = [
    { key: "name", header: "Channel", stackTitle: true, render: (_, row) => <ChannelName channel={row} channels={channels} /> },
    { key: "integrationKind", header: "Goes out through", width: "150px", render: (_, row) => <span className="text-ink-muted">{integrationLabel(row.integrationKind)}</span> },
    { key: "connectionState", header: "Your connection", width: "150px", render: (_, row) => <ConnectionBadge state={row.connectionState} /> },
    usageColumn,
    statusColumn,
    {
      key: "connect",
      header: "Connect",
      srHeader: true,
      interactive: true,
      width: "120px",
      render: (_, row) =>
        row.connectionState === GtmConnectionState.CONNECTED ? (
          <Button variant="ghost" size="xs" href={connectorsPath()} aria-label={`Manage ${integrationLabel(row.integrationKind)} for ${row.name}`}>
            Manage
          </Button>
        ) : (
          <Button variant="primary" size="xs" leftIcon={<Plug />} href={connectorsPath()} aria-label={`Connect ${integrationLabel(row.integrationKind)} for ${row.name}`}>
            Connect
          </Button>
        ),
    },
    editColumn,
  ];

  const manualColumns: DataTableColumn<ChannelRow>[] = [
    {
      key: "name",
      header: "Channel",
      stackTitle: true,
      render: (_, row) => <ChannelName channel={row} channels={channels} tag={<ChannelSourceTag source={row.source} />} />,
    },
    {
      key: "manualInstructionsMd",
      header: "How it goes out",
      render: (_, row) => (
        <span className="block space-y-1.5 whitespace-normal">
          <span className="block text-ink-muted">{row.manualInstructionsMd || "Posted by hand."}</span>
          {row.channelTypes.length > 0 && (
            <span className="flex flex-wrap gap-1" aria-label="Channel types">
              {row.channelTypes.map((t) => (
                <Tag key={t}>{t}</Tag>
              ))}
            </span>
          )}
        </span>
      ),
    },
    usageColumn,
    statusColumn,
    editColumn,
  ];

  return (
    <>
      {/* The action wraps under the title on a phone: PageHeader keeps actions
          beside it and squeezes the meta line to a column. */}
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
        <PageHeader title="Channels" meta={<span>Where your posts, emails and videos go out, and how.</span>} className="" />
        <Button variant="primary" size="sm" leftIcon={<Plus />} onClick={() => setEditing(null)} aria-haspopup="dialog">
          New channel
        </Button>
      </div>

      {channelsQ.isError ? (
        <EmptyState title="Couldn't load channels" description="Something went wrong reading this org's channels." actionLabel="Try again" onAction={() => void channelsQ.refetch()} />
      ) : (
        <>
          <section aria-labelledby="gtm-integrated" className="space-y-3" data-testid="gtm-channels-integrated">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <h2 id="gtm-integrated" className="text-base font-semibold text-ink">
                Integrated
              </h2>
              <span className="inline-flex flex-wrap items-center gap-2 text-sm text-ink-muted">
                Go out through a Reliant integration, after a person approves. Connecting one is yours to do: <PersonOnlyTag />
              </span>
            </div>
            <DataTable
              columns={integratedColumns}
              data={integrated}
              loading={channelsQ.isPending}
              getRowKey={(row) => row.id}
              layout="fixed"
              stacked
              ariaLabel="Integrated channels"
              emptyMessage="No integrated channels."
            />
          </section>

          <section aria-labelledby="gtm-manual" className="space-y-3" data-testid="gtm-channels-manual">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <h2 id="gtm-manual" className="text-base font-semibold text-ink">
                Manual
              </h2>
              <span className="text-sm text-ink-muted">Posted by hand. The item records the URL once it is out. Agents may add these.</span>
            </div>
            <DataTable
              columns={manualColumns}
              data={manual}
              loading={channelsQ.isPending}
              getRowKey={(row) => row.id}
              layout="fixed"
              stacked
              ariaLabel="Manual channels"
              emptyMessage="No manual channels yet."
            />
          </section>
        </>
      )}

      {editing === null && <ChannelForm key="new" onClose={() => setEditing(undefined)} />}
      {editingChannel && (
        // Keyed on the revision: a conflict re-reads the list, and the form starts again from the current version.
        <ChannelForm key={`${editingChannel.id}@${editingChannel.revision}`} channel={editingChannel} onClose={() => setEditing(undefined)} />
      )}
    </>
  );
}
