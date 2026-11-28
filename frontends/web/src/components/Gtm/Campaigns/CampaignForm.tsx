// Copyright (c) 2026 Reliant Labs

/**
 * Create or edit a campaign: its strategy (every campaign belongs to one),
 * name, goal, period and status, and what it plans: the channels it means to
 * show up on and the formats it means to use, which its coverage holds
 * against its items. An edit names the revision it read (expected_revision)
 * and sends the planned lists whole (GtmChannelIdList, GtmItemFormatList).
 */

import { create } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { useState } from "react";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import Textarea from "@/components/forge-ui/textarea";
import {
  GtmCampaignStatus,
  GtmChannelIdListSchema,
  GtmChannelStatus,
  GtmItemFormatListSchema,
  type GtmCampaign,
  type GtmChannel,
  type GtmItemFormat,
} from "@/gen/reliant/v1/gtm_pb";
import { useCreateCampaign, useListStrategies, useUpdateCampaign } from "@/hooks/gtm-service-hooks_gen";

import { useGtmOrgId } from "../shared/GtmShell";
import { CAMPAIGN_STATUS, formatLabel, ITEM_FORMATS } from "../shared/ui";
import { useGtmWriteError } from "../shared/writes";

const SELECT =
  "h-9 w-full rounded-md border border-border-strong bg-surface px-2.5 text-sm text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

const STATUSES = [GtmCampaignStatus.DRAFT, GtmCampaignStatus.ACTIVE, GtmCampaignStatus.DONE, GtmCampaignStatus.ARCHIVED];

/** A timestamp as the YYYY-MM-DD a date input holds (local day); "" when unset. */
export function dayOf(ts: Timestamp | undefined): string {
  if (!ts) return "";
  const d = timestampDate(ts);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** A date input's YYYY-MM-DD as the start of that local day. */
export function tsOfDay(day: string): Timestamp | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  return m ? timestampFromDate(new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]))) : undefined;
}

export function CampaignForm({
  campaign,
  channels,
  defaultStrategyId,
  onClose,
  onSaved,
}: {
  /** The campaign to edit; none to create one. */
  campaign?: GtmCampaign;
  channels: GtmChannel[];
  defaultStrategyId?: string;
  onClose: () => void;
  onSaved?: (id: string) => void;
}) {
  const orgId = useGtmOrgId();
  const strategiesQ = useListStrategies({ orgId, pageSize: 200 }, { enabled: !!orgId });
  const strategies = strategiesQ.data?.strategies ?? [];
  const [name, setName] = useState(campaign?.name ?? "");
  const [goal, setGoal] = useState(campaign?.goal ?? "");
  const [strategyId, setStrategyId] = useState(campaign?.strategyId ?? defaultStrategyId ?? "");
  const [start, setStart] = useState(dayOf(campaign?.periodStart));
  const [end, setEnd] = useState(dayOf(campaign?.periodEnd));
  const [status, setStatus] = useState(campaign?.status ?? GtmCampaignStatus.DRAFT);
  const [plannedChannels, setPlannedChannels] = useState<string[]>(campaign?.plannedChannelIds ?? []);
  const [plannedFormats, setPlannedFormats] = useState<GtmItemFormat[]>(campaign?.plannedFormats ?? []);
  const onError = useGtmWriteError();
  const done = (id: string) => {
    onSaved?.(id);
    onClose();
  };
  const createC = useCreateCampaign({ onSuccess: (r) => done(r.campaign?.id ?? ""), onError });
  const updateC = useUpdateCampaign({ onSuccess: (r) => done(r.campaign?.id ?? ""), onError });
  const busy = createC.isPending || updateC.isPending;
  const strategyId_ = strategyId || strategies[0]?.id || "";
  const badPeriod = start !== "" && end !== "" && end < start;
  const valid = name.trim() !== "" && strategyId_ !== "" && !badPeriod;

  function save() {
    if (!campaign) {
      createC.mutate({
        orgId,
        strategyId: strategyId_,
        name: name.trim(),
        goal,
        periodStart: tsOfDay(start),
        periodEnd: tsOfDay(end),
        status,
        plannedChannelIds: plannedChannels,
        plannedFormats,
      });
      return;
    }
    updateC.mutate({
      orgId,
      id: campaign.id,
      expectedRevision: campaign.revision,
      name: name.trim(),
      goal,
      strategyId: strategyId_,
      status,
      ...(start ? { periodStart: tsOfDay(start) } : { clearPeriodStart: campaign.periodStart !== undefined }),
      ...(end ? { periodEnd: tsOfDay(end) } : { clearPeriodEnd: campaign.periodEnd !== undefined }),
      plannedChannelIds: create(GtmChannelIdListSchema, { ids: plannedChannels }),
      plannedFormats: create(GtmItemFormatListSchema, { formats: plannedFormats }),
    });
  }

  const toggle = <T,>(list: T[], value: T) => (list.includes(value) ? list.filter((v) => v !== value) : [...list, value]);
  const pickable = channels.filter((c) => c.status !== GtmChannelStatus.ARCHIVED || plannedChannels.includes(c.id));

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      sheetOnMobile
      title={campaign ? "Edit campaign" : "New campaign"}
      description={campaign?.name}
      footer={
        <div className="flex w-full justify-end gap-2">
          <Button variant="secondary" size="sm" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" size="sm" onClick={save} loading={busy} disabled={!valid}>
            {campaign ? "Save" : "Create campaign"}
          </Button>
        </div>
      }
    >
      <div className="space-y-4 text-sm" data-testid="gtm-campaign-form">
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Name</span>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Strategy</span>
            <select className={SELECT} value={strategyId_} onChange={(e) => setStrategyId(e.target.value)} aria-label="Strategy">
              {strategies.length === 0 && <option value="">No strategy yet: create one on the Strategy page</option>}
              {strategies.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </select>
          </label>
        </div>
        <label className="block space-y-1.5">
          <span className="font-medium text-ink">Goal</span>
          <Textarea value={goal} onChange={(e) => setGoal(e.target.value)} rows={2} />
        </label>
        <div className="grid gap-4 sm:grid-cols-3">
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Starts</span>
            <Input type="date" value={start} onChange={(e) => setStart(e.target.value)} />
          </label>
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Ends</span>
            <Input type="date" value={end} onChange={(e) => setEnd(e.target.value)} />
          </label>
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Status</span>
            <select className={SELECT} value={status} onChange={(e) => setStatus(Number(e.target.value) as GtmCampaignStatus)} aria-label="Status">
              {STATUSES.map((s) => (
                <option key={s} value={s}>
                  {CAMPAIGN_STATUS[s]?.label}
                </option>
              ))}
            </select>
          </label>
        </div>
        {badPeriod && <p className="text-danger-ink">The end is before the start.</p>}
        <fieldset className="space-y-2">
          <legend className="font-medium text-ink">Planned channels</legend>
          <div className="flex flex-wrap gap-x-4 gap-y-2">
            {pickable.map((c) => (
              <label key={c.id} className="inline-flex items-center gap-2 text-ink">
                <input type="checkbox" checked={plannedChannels.includes(c.id)} onChange={() => setPlannedChannels(toggle(plannedChannels, c.id))} />
                {c.name}
              </label>
            ))}
          </div>
        </fieldset>
        <fieldset className="space-y-2">
          <legend className="font-medium text-ink">Planned formats</legend>
          <div className="flex flex-wrap gap-x-4 gap-y-2">
            {ITEM_FORMATS.map((f) => (
              <label key={f} className="inline-flex items-center gap-2 text-ink">
                <input type="checkbox" checked={plannedFormats.includes(f)} onChange={() => setPlannedFormats(toggle(plannedFormats, f))} />
                {formatLabel(f)}
              </label>
            ))}
          </div>
          <p className="text-xs text-ink-muted">Coverage pairs each planned channel with each planned format. Plan only one side and the other reads as any.</p>
        </fieldset>
      </div>
    </Modal>
  );
}
