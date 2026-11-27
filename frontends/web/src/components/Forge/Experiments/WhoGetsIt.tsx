// Copyright (c) 2025 Reliant Labs

/**
 * "Who gets it?" — the controls that decide a change's audience, and the
 * actions that apply it: give everyone a version (remote config), roll it
 * out to a share (gradual rollout), or compare versions (an experiment with
 * one success metric).
 *
 * Two kinds of state live here, and the copy keeps them apart:
 *   - the DRAFT (what stays together, who is eligible, the split, the
 *     metric) is what the next launch freezes. "Save draft" writes it, and a
 *     launch or a roll-out that starts a run saves it first when it changed;
 *   - the LIVE percentage moves within a run (Ramp): raising it only adds
 *     people, lowering it and raising it again returns the same people.
 *
 * Every write goes through `onWrite`, which owns the revision check, the
 * approval flow and the conflict notice (ChangeDetail).
 */

import { useId, useMemo, useState } from "react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import { NO_REFETCH, useChangeWrites, type ChangeReply } from "@/hooks/changes-queries";
import { launchChangeRequest, rampChangeRequest, rollOutChangeRequest, updateChangeDraftRequest } from "@/services/controlPlane/changes/changesRequests";
import type { Change, ChangeAudience, ChangeDraftEdit, ChangeMetric, ChangeSplitArm, ChangeUnit } from "@/services/controlPlane/changes/changesTypes";

import { FIELD, segmentTone, SEGMENTED } from "../controlStyles";
import { useObsReadOnly } from "../Observability/obsReadOnly";
import { evenSplit, formatBp, parsePercent, splitTotal, UNIT_LABELS, WHO_GETS_IT, whoGetsItOf, type WhoGetsIt as Who } from "./changeModel";

/** One write: a label for the approval and busy states, and the call (a step-up code when retried after approval). */
export type ChangeWrite = (label: string, write: (stepUpCode?: string) => Promise<ChangeReply>) => Promise<Change | null>;

export interface WhoGetsItProps {
  change: Change;
  initialWho?: Who;
  busy: string | null;
  onWrite: ChangeWrite;
}

interface Draft {
  unit: ChangeUnit;
  eligible: "everyone" | "attribute";
  attribute: string;
  values: string;
  split: ChangeSplitArm[];
  metricEvent: string;
  metricProp: string;
  metricPropValue: string;
  metricWindow: string;
  metricMinSample: string;
  metricControl: string;
}

function draftOf(change: Change, variants: string[]): Draft {
  const m = change.metric;
  return {
    unit: change.unit,
    eligible: change.audience ? "attribute" : "everyone",
    attribute: change.audience?.attribute ?? "",
    values: change.audience?.in.join(", ") ?? "",
    split: change.split.length > 0 ? change.split : evenSplit(variants),
    metricEvent: m?.event ?? "",
    metricProp: m?.prop ?? "",
    metricPropValue: m?.propValue ?? "",
    metricWindow: m?.windowHours ? String(m.windowHours) : "",
    metricMinSample: m?.minSamplePerArm ? String(m.minSamplePerArm) : "",
    // "" is the server's default: the declared default version.
    metricControl: m?.controlVariant ?? "",
  };
}

function audienceOf(d: Draft): ChangeAudience | null {
  if (d.eligible === "everyone") return null;
  const values = d.values
    .split(",")
    .map((v) => v.trim())
    .filter(Boolean);
  return { attribute: d.attribute.trim(), in: values };
}

function metricOf(d: Draft): ChangeMetric | null {
  if (!d.metricEvent.trim()) return null;
  return {
    event: d.metricEvent.trim(),
    prop: d.metricProp.trim(),
    propValue: d.metricPropValue.trim(),
    windowHours: Number(d.metricWindow) || 0,
    minSamplePerArm: Number(d.metricMinSample) || 0,
    controlVariant: d.metricControl,
  };
}

const sameAudience = (a: ChangeAudience | null, b: ChangeAudience | null) => JSON.stringify(a) === JSON.stringify(b);
const sameMetric = (a: ChangeMetric | null, b: ChangeMetric | null) =>
  JSON.stringify(a && { ...a, controlVariant: a.controlVariant || "" }) === JSON.stringify(b && { ...b, controlVariant: b.controlVariant || "" });

/** The draft edit `d` makes to `change`, or null when it makes none. */
export function draftEdit(change: Change, d: Draft, who: Who): ChangeDraftEdit | null {
  const edit: ChangeDraftEdit = {};
  if (d.unit !== change.unit) edit.unit = d.unit;
  const audience = audienceOf(d);
  if (!sameAudience(audience, change.audience)) edit.audience = audience;
  if (who === "compare") {
    if (JSON.stringify(d.split) !== JSON.stringify(change.split)) edit.split = d.split;
    const metric = metricOf(d);
    if (!sameMetric(metric, change.metric)) edit.metric = metric;
  }
  return Object.keys(edit).length > 0 ? edit : null;
}

export function WhoGetsIt({ change, initialWho, busy, onWrite }: WhoGetsItProps) {
  const writes = useChangeWrites();
  const id = useId();
  // The demo console: every write is off, and the action row says why (obsReadOnly.tsx).
  const lock = useObsReadOnly();
  const variants = useMemo(() => change.declaration?.variants.map((v) => v.name) ?? [], [change.declaration]);
  const defaultVariant = change.declaration?.defaultVariant ?? "";
  const newVariant = variants.find((v) => v !== defaultVariant) ?? variants[0] ?? "";
  const [who, setWho] = useState<Who>(initialWho ?? whoGetsItOf(change));
  const [variant, setVariant] = useState(change.everyoneVariant || change.rolloutVariant || newVariant);
  const [percent, setPercent] = useState(change.percentBp > 0 ? formatBp(change.percentBp) : "5");
  const [draft, setDraft] = useState<Draft>(() => draftOf(change, variants));

  // A new revision from the server (a write, a conflict refetch) resets the
  // form to it. Keyed on the revision, not the object: a background refetch
  // of the same revision must not wipe what the reader is typing.
  const [seen, setSeen] = useState(`${change.id}@${change.revision}`);
  if (seen !== `${change.id}@${change.revision}`) {
    setSeen(`${change.id}@${change.revision}`);
    setDraft(draftOf(change, variants));
    if (change.percentBp > 0) setPercent(formatBp(change.percentBp));
  }

  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));
  const percentBp = parsePercent(percent);
  const edit = draftEdit(change, draft, who);
  const declared = !!change.declaration;
  const running = !!change.currentRun;
  const audience = audienceOf(draft);
  const audienceInvalid = !!audience && (!audience.attribute || audience.in.length === 0);
  const splitInvalid = who === "compare" && splitTotal(draft.split) !== 10000;
  const metricMissing = who === "compare" && !draft.metricEvent.trim();
  const isBusy = busy !== null || lock.readOnly;
  // A running rollout's percentage moves by a ramp, which names no version;
  // another version is a new roll-out of it (and is approved as one).
  const rampsRollout = change.mode === "rollout" && running && variant === change.rolloutVariant;

  /** Save the draft if it changed; the revision the next write must name. Null when the save failed. */
  const saveDraft = async (): Promise<number | null> => {
    if (!edit) return change.revision;
    const saved = await onWrite("Save draft", () => writes.updateChangeDraft(updateChangeDraftRequest(change.id, change.revision, edit), NO_REFETCH));
    return saved ? saved.revision : null;
  };

  const launch = async () => {
    const revision = await saveDraft();
    if (revision === null || percentBp === null) return;
    await onWrite(running ? "Relaunch" : "Launch", (stepUpCode) =>
      writes.launchChange(launchChangeRequest(change.id, percentBp, { expectedRevision: revision, stepUpCode }), NO_REFETCH)
    );
  };
  const ramp = () =>
    percentBp !== null && onWrite(`Change to ${formatBp(percentBp)}%`, (stepUpCode) =>
      writes.rampChange(rampChangeRequest(change.id, percentBp, { expectedRevision: change.revision, stepUpCode }), NO_REFETCH)
    );
  const rollOut = async (everyone: boolean) => {
    const revision = everyone || running ? change.revision : await saveDraft();
    if (revision === null || (!everyone && percentBp === null)) return;
    await onWrite(everyone ? `Give everyone ${variant}` : `Roll out to ${formatBp(percentBp ?? 0)}%`, (stepUpCode) =>
      writes.rollOutChange(rollOutChangeRequest(change.id, { expectedRevision: revision, stepUpCode, variant, everyone, percentBp: percentBp ?? 0 }), NO_REFETCH)
    );
  };

  const variantSelect = (
    <label className="flex items-center gap-2 text-ui text-ink">
      Version
      <select value={variant} onChange={(event) => setVariant(event.target.value)} className={FIELD} data-testid="who-variant" disabled={!declared}>
        {variants.map((v) => (
          <option key={v} value={v}>
            {v}
            {v === defaultVariant ? " (default)" : ""}
          </option>
        ))}
      </select>
    </label>
  );
  const percentInput = (
    <label className="flex items-center gap-2 text-ui text-ink">
      How many
      <span className="relative">
        <input
          inputMode="decimal"
          value={percent}
          onChange={(event) => setPercent(event.target.value)}
          className={`${FIELD} w-20 pr-6 tabular-nums`}
          aria-label="How many, percent"
          aria-invalid={percentBp === null}
          data-testid="who-percent"
        />
        <span className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-sm text-ink-muted">%</span>
      </span>
    </label>
  );

  const actionReason = !declared
    ? "Launching waits for a release that declares this change."
    : percentBp === null && who !== "everyone"
      ? "Enter a percentage from 0 to 100."
      : audienceInvalid
        ? "Name the attribute and at least one value."
        : splitInvalid
          ? "The split must add up to 100%."
          : metricMissing
            ? "Pick a success metric to compare against."
            : null;

  return (
    <Card padding="none" aria-labelledby={`${id}-title`}>
      <div className="space-y-4 p-4">
        <div className="space-y-1.5">
          <h2 id={`${id}-title`} className="text-section text-ink">
            Who gets it?
          </h2>
          <div className={SEGMENTED} role="radiogroup" aria-label="Who gets it?">
            {WHO_GETS_IT.map((option) => (
              <button
                key={option.value}
                type="button"
                role="radio"
                aria-checked={who === option.value}
                onClick={() => setWho(option.value)}
                className={`rounded-md px-3 py-1 text-sm ${segmentTone(who === option.value)}`}
              >
                {option.label}
              </button>
            ))}
          </div>
          <p className="text-meta text-ink-muted">{WHO_GETS_IT.find((option) => option.value === who)?.hint}</p>
        </div>

        {who !== "everyone" && (
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <label htmlFor={`${id}-unit`} className="block text-ui font-medium text-ink">
                What stays together
              </label>
              <select id={`${id}-unit`} value={draft.unit} onChange={(event) => set({ unit: event.target.value as ChangeUnit })} className={`${FIELD} w-full`}>
                {(Object.keys(UNIT_LABELS) as ChangeUnit[]).map((u) => (
                  <option key={u} value={u}>
                    {UNIT_LABELS[u].label}
                  </option>
                ))}
              </select>
              <p className="text-meta text-ink-muted">{UNIT_LABELS[draft.unit].hint}</p>
            </div>
            <div className="space-y-1.5">
              <label htmlFor={`${id}-eligible`} className="block text-ui font-medium text-ink">
                Who can be included
              </label>
              <select
                id={`${id}-eligible`}
                value={draft.eligible}
                onChange={(event) => set({ eligible: event.target.value as Draft["eligible"] })}
                className={`${FIELD} w-full`}
              >
                <option value="everyone">Everyone</option>
                <option value="attribute">Only people with a certain value</option>
              </select>
              {draft.eligible === "attribute" && (
                <div className="flex flex-wrap items-center gap-2">
                  <input
                    value={draft.attribute}
                    onChange={(event) => set({ attribute: event.target.value })}
                    placeholder="plan"
                    aria-label="Attribute"
                    className={`${FIELD} w-28 font-mono`}
                  />
                  <span className="text-sm text-ink-muted">is one of</span>
                  <input
                    value={draft.values}
                    onChange={(event) => set({ values: event.target.value })}
                    placeholder="pro, team"
                    aria-label="Values, comma separated"
                    className={`${FIELD} min-w-0 flex-1`}
                  />
                </div>
              )}
            </div>
          </div>
        )}

        {who === "compare" && declared && (
          <div className="space-y-4">
            <div className="space-y-1.5">
              <p className="text-ui font-medium text-ink">Split</p>
              <div className="flex flex-wrap gap-3">
                {draft.split.map((arm, i) => (
                  <label key={arm.variant} className="flex items-center gap-2 text-ui text-ink">
                    <span className="font-mono">{arm.variant}</span>
                    <input
                      inputMode="decimal"
                      value={formatBp(arm.weightBp)}
                      onChange={(event) => {
                        const bp = parsePercent(event.target.value) ?? 0;
                        set({ split: draft.split.map((a, j) => (j === i ? { ...a, weightBp: bp } : a)) });
                      }}
                      aria-label={`${arm.variant} share, percent`}
                      className={`${FIELD} w-16 tabular-nums`}
                    />
                    <span className="text-sm text-ink-muted">%</span>
                  </label>
                ))}
              </div>
              {splitInvalid && <p className="text-meta text-danger-ink">The split adds up to {formatBp(splitTotal(draft.split))}%, not 100%.</p>}
            </div>
            <MetricPicker draft={draft} set={set} variants={draft.split.map((a) => a.variant)} defaultVariant={defaultVariant} />
          </div>
        )}

        {edit && (
          <div className="flex items-center gap-3">
            <Button variant="outline" size="sm" onClick={() => void saveDraft()} disabled={isBusy || audienceInvalid || splitInvalid} loading={busy === "Save draft"}>
              Save draft
            </Button>
            <span className="text-meta text-ink-muted">
              {running ? "The running version keeps its settings until you relaunch." : "Unsaved changes. Launching saves them first."}
            </span>
          </div>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-3 border-t border-border px-4 py-3" data-testid="who-actions">
        {who === "everyone" && (
          <>
            {variantSelect}
            <Button variant="primary" onClick={() => void rollOut(true)} disabled={isBusy || !declared || !variant} loading={busy?.startsWith("Give everyone")}>
              Give everyone {variant || "this version"}
            </Button>
          </>
        )}
        {who === "some" && (
          <>
            {variantSelect}
            {percentInput}
            {rampsRollout ? (
              <Button variant="primary" onClick={() => void ramp()} disabled={isBusy || !!actionReason} loading={busy?.startsWith("Change to")}>
                Change to {formatBp(percentBp ?? 0)}%
              </Button>
            ) : (
              <Button variant="primary" onClick={() => void rollOut(false)} disabled={isBusy || !!actionReason || !variant} loading={busy?.startsWith("Roll out")}>
                Roll out to {formatBp(percentBp ?? 0)}%
              </Button>
            )}
          </>
        )}
        {who === "compare" && (
          <>
            {percentInput}
            {change.mode === "experiment" && running ? (
              <>
                <Button variant="primary" onClick={() => void ramp()} disabled={isBusy || percentBp === null} loading={busy?.startsWith("Change to")}>
                  Change to {formatBp(percentBp ?? 0)}%
                </Button>
                <Button variant="outline" onClick={() => void launch()} disabled={isBusy || !!actionReason} loading={busy === "Relaunch"}>
                  Relaunch with these settings
                </Button>
              </>
            ) : (
              <Button variant="primary" onClick={() => void launch()} disabled={isBusy || !!actionReason} loading={busy === "Launch"} data-testid="who-launch">
                Launch
              </Button>
            )}
          </>
        )}
        {lock.readOnly ? (
          <span className="text-meta text-ink-muted" data-testid="who-read-only">{lock.reason}</span>
        ) : (
          actionReason && <span className="text-meta text-ink-muted">{actionReason}</span>
        )}
        {change.approvalRequired && !actionReason && !lock.readOnly && <span className="text-meta text-ink-muted">This environment needs a person's approval to give this to more people.</span>}
      </div>
    </Card>
  );
}

function MetricPicker({ draft, set, variants, defaultVariant }: { draft: Draft; set: (patch: Partial<Draft>) => void; variants: string[]; defaultVariant: string }) {
  const id = useId();
  return (
    <fieldset className="space-y-2">
      <legend className="text-ui font-medium text-ink">Success metric</legend>
      <p className="text-meta text-ink-muted">The share of people in each version who do this within the window. Use an event your app already tracks.</p>
      <div className="flex flex-wrap items-center gap-2">
        <input
          value={draft.metricEvent}
          onChange={(event) => set({ metricEvent: event.target.value })}
          placeholder="checkout_completed"
          aria-label="Event name"
          className={`${FIELD} w-56 font-mono`}
          data-testid="metric-event"
        />
        <span className="text-sm text-ink-muted">where</span>
        <input value={draft.metricProp} onChange={(event) => set({ metricProp: event.target.value })} placeholder="property" aria-label="Property (optional)" className={`${FIELD} w-28 font-mono`} />
        <span className="text-sm text-ink-muted">=</span>
        <input value={draft.metricPropValue} onChange={(event) => set({ metricPropValue: event.target.value })} placeholder="value" aria-label="Property value (optional)" className={`${FIELD} w-28`} />
      </div>
      <div className="flex flex-wrap items-center gap-4">
        <label htmlFor={`${id}-window`} className="flex items-center gap-2 text-ui text-ink">
          Within
          <input id={`${id}-window`} inputMode="numeric" value={draft.metricWindow} onChange={(event) => set({ metricWindow: event.target.value.replace(/\D/g, "") })} placeholder="168" className={`${FIELD} w-20 tabular-nums`} />
          hours
        </label>
        <label htmlFor={`${id}-min`} className="flex items-center gap-2 text-ui text-ink">
          At least
          <input id={`${id}-min`} inputMode="numeric" value={draft.metricMinSample} onChange={(event) => set({ metricMinSample: event.target.value.replace(/\D/g, "") })} placeholder="default" className={`${FIELD} w-24 tabular-nums`} />
          people per version before deciding
        </label>
        <label htmlFor={`${id}-control`} className="flex items-center gap-2 text-ui text-ink">
          Compared against
          <select id={`${id}-control`} value={draft.metricControl} onChange={(event) => set({ metricControl: event.target.value })} className={FIELD}>
            <option value="">{defaultVariant ? `${defaultVariant} (the default)` : "the default"}</option>
            {variants.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
        </label>
      </div>
    </fieldset>
  );
}
