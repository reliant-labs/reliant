// Copyright (c) 2026 Reliant Labs

/**
 * Create or edit a channel (CreateChannel / UpdateChannel).
 *
 * A MANUAL channel's fields (name, key, instructions, channel types, status)
 * are anyone's: an agent may set them too. A channel's mode and its
 * integration (integration_kind, connection_id) are a person's to set, and
 * every web user is a person, so the form shows them. Connecting the
 * integration itself is not here: that is Reliant's connect flow, on the
 * Connectors page. An edit names the revision it read and sends
 * channel_types whole (GtmChannelTypeList). A key the org already holds is
 * refused, and the form says so.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { useState } from "react";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import Textarea from "@/components/forge-ui/textarea";
import { GtmChannelMode, GtmChannelStatus, GtmChannelTypeListSchema, type GtmChannel } from "@/gen/reliant/v1/gtm_pb";
import { useCreateChannel, useUpdateChannel } from "@/hooks/gtm-service-hooks_gen";

import { useGtmOrgId } from "../shared/GtmShell";
import { useGtmWriteError } from "../shared/writes";

const SELECT =
  "h-9 w-full rounded-md border border-border-strong bg-surface px-2.5 text-sm text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

const SLUG = /^[a-z0-9-]{1,32}$/;

/** "Hacker News!" → "hacker-news": a key from a name, for a new channel. */
export const slugOf = (name: string) =>
  name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 32);

/** "forum, community" → ["forum", "community"]. */
export const typesOf = (text: string) => [...new Set(text.split(/[,\s]+/).map((t) => t.trim().toLowerCase()).filter(Boolean))];

export const STATUS_LABEL: Record<number, string> = {
  [GtmChannelStatus.ACTIVE]: "Active",
  [GtmChannelStatus.PAUSED]: "Paused",
  [GtmChannelStatus.ARCHIVED]: "Archived",
};

export function ChannelForm({ channel, onClose }: { channel?: GtmChannel; onClose: () => void }) {
  const orgId = useGtmOrgId();
  const [name, setName] = useState(channel?.name ?? "");
  const [key, setKey] = useState(channel?.key ?? "");
  const [keyTouched, setKeyTouched] = useState(!!channel);
  const [mode, setMode] = useState<GtmChannelMode>(channel?.mode ?? GtmChannelMode.MANUAL);
  const [integrationKind, setIntegrationKind] = useState(channel?.integrationKind ?? "");
  const [connectionId, setConnectionId] = useState(channel?.connectionId ?? "");
  const [instructions, setInstructions] = useState(channel?.manualInstructionsMd ?? "");
  const [types, setTypes] = useState((channel?.channelTypes ?? []).join(", "));
  const [status, setStatus] = useState<GtmChannelStatus>(channel?.status ?? GtmChannelStatus.ACTIVE);
  const [keyTaken, setKeyTaken] = useState(false);
  const onWriteError = useGtmWriteError();
  const onError = (error: unknown) => {
    if (error instanceof ConnectError && error.code === Code.AlreadyExists) setKeyTaken(true);
    else onWriteError(error);
  };
  const createC = useCreateChannel({ onSuccess: onClose, onError });
  const updateC = useUpdateChannel({ onSuccess: onClose, onError });
  const busy = createC.isPending || updateC.isPending;

  const effectiveKey = keyTouched ? key : slugOf(name);
  const typeList = typesOf(types);
  const integrated = mode === GtmChannelMode.INTEGRATED;
  const problems = [
    !name.trim() && "a name",
    !SLUG.test(effectiveKey) && "a key of lowercase letters, digits and dashes (up to 32)",
    typeList.some((t) => !SLUG.test(t)) && "channel types that are lowercase slugs",
    integrated && !integrationKind.trim() && "the integration it goes out through",
  ].filter(Boolean) as string[];

  function save() {
    setKeyTaken(false);
    const integration = integrated ? { integrationKind: integrationKind.trim(), connectionId: connectionId.trim() } : { integrationKind: "", connectionId: "" };
    if (!channel) {
      createC.mutate({ orgId, name: name.trim(), key: effectiveKey, mode, ...integration, manualInstructionsMd: integrated ? "" : instructions, status, channelTypes: typeList });
      return;
    }
    updateC.mutate({
      orgId,
      id: channel.id,
      expectedRevision: channel.revision,
      name: name.trim(),
      key: effectiveKey,
      mode,
      ...integration,
      manualInstructionsMd: integrated ? "" : instructions,
      status,
      channelTypes: create(GtmChannelTypeListSchema, { types: typeList }),
    });
  }

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      sheetOnMobile
      title={channel ? "Edit channel" : "New channel"}
      description={channel?.name}
      footer={
        <div className="flex w-full flex-wrap items-center justify-between gap-3">
          <p className="text-xs text-ink-muted">{problems.length ? `Needs ${problems.join(", ")}.` : ""}</p>
          <div className="flex gap-2">
            <Button variant="secondary" size="sm" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="primary" size="sm" onClick={save} loading={busy} disabled={problems.length > 0}>
              {channel ? "Save" : "Add channel"}
            </Button>
          </div>
        </div>
      }
    >
      <div className="space-y-4 text-sm" data-testid="gtm-channel-form">
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Name</span>
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Key</span>
            <Input
              value={effectiveKey}
              onChange={(e) => {
                setKeyTouched(true);
                setKey(e.target.value);
                setKeyTaken(false);
              }}
              className="font-mono"
            />
          </label>
        </div>
        {keyTaken && (
          <p className="text-danger-ink" role="alert">
            Another channel already has the key {effectiveKey}. Pick another.
          </p>
        )}
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">How it goes out</span>
            <select className={SELECT} value={mode} onChange={(e) => setMode(Number(e.target.value) as GtmChannelMode)} aria-label="How it goes out">
              <option value={GtmChannelMode.MANUAL}>Manual: a person posts by hand</option>
              <option value={GtmChannelMode.INTEGRATED}>Integrated: through a Reliant integration</option>
            </select>
          </label>
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Status</span>
            <select className={SELECT} value={status} onChange={(e) => setStatus(Number(e.target.value) as GtmChannelStatus)} aria-label="Status">
              {[GtmChannelStatus.ACTIVE, GtmChannelStatus.PAUSED, GtmChannelStatus.ARCHIVED].map((s) => (
                <option key={s} value={s}>
                  {STATUS_LABEL[s]}
                </option>
              ))}
            </select>
          </label>
        </div>
        {integrated ? (
          <div className="grid gap-4 rounded-lg border border-border p-3 sm:grid-cols-2" data-testid="gtm-channel-integration">
            <label className="block space-y-1.5">
              <span className="font-medium text-ink">Integration</span>
              <Input value={integrationKind} onChange={(e) => setIntegrationKind(e.target.value)} placeholder="gmail" className="font-mono" />
            </label>
            <label className="block space-y-1.5">
              <span className="font-medium text-ink">Connection</span>
              <Input value={connectionId} onChange={(e) => setConnectionId(e.target.value)} placeholder="Your default connection" className="font-mono" />
            </label>
            <p className="text-xs text-ink-muted sm:col-span-2">
              Only a person sets these. Leave the connection empty to use each person&apos;s own default connection for the integration. Connect it on the
              Connectors page.
            </p>
          </div>
        ) : (
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">How to post on it</span>
            <Textarea value={instructions} onChange={(e) => setInstructions(e.target.value)} rows={3} placeholder="Post by hand, then paste the URL on the item." />
          </label>
        )}
        <div className="space-y-1.5">
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Channel types</span>
            <Input value={types} onChange={(e) => setTypes(e.target.value)} placeholder="forum, community" aria-describedby="gtm-channel-types-hint" />
          </label>
          <p id="gtm-channel-types-hint" className="text-xs text-ink-muted">
            What kind of channel it is, as short slugs. A library strategy plans its first campaign on channels whose types fit.
          </p>
        </div>
      </div>
    </Modal>
  );
}
