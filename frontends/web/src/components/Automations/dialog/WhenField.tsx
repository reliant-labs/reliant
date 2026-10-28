// Copyright (c) 2025 Reliant Labs

/**
 * WHEN, the dialog's first and defining choice (report 03 §6.1): a schedule
 * sentence, an app event (GitHub, Slack, Gmail, Twilio — any catalog trigger
 * type), a webhook, or another workflow's run ending.
 *
 * The app events come from the builder palette's Triggers list, so there is
 * one catalog of trigger kinds, not a second one for this dialog. An app
 * event's account (and who it writes as) sits right here beside it, because
 * that is the connection the event arrives through.
 */

import { useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";

import { fetchCatalogEntry, useCatalogEntry, useDeclaredTriggerRef, type CatalogEntry, type CatalogEntrySummary } from "@/hooks/catalog-queries";
import type { ActingIdentityKey } from "@/hooks/trigger-model";
import { integrationOf, triggerFromCatalog } from "@/lib/declaredTriggers";
import { TriggerSourceFields } from "../../workflow/config/DeclaredTriggerPanel";
import { OnlyFromControl } from "../../workflow/config/OnlyFromControl";
import { ConnectionPicker } from "../../workflow/connections/ConnectionPicker";
import { ConnectIntegrationDialog } from "../../workflow/connections/ConnectIntegrationDialog";
import { StepPalette } from "../../workflow/palette/StepPalette";
import { errorTextClass, fieldClass, hintClass, labelClass } from "../../workflow/run/runFormStyles";
import { ActingIdentityField } from "../ActingIdentityField";
import { ScheduleSentence } from "./ScheduleSentence";
import { withKind, type WhenKind, type WhenState } from "./whenModel";

const KIND_OPTIONS: Array<{ value: WhenKind; label: string }> = [
  { value: "schedule", label: "On a schedule" },
  { value: "integration", label: "When something happens in an app" },
  { value: "webhook", label: "When a webhook is called" },
  { value: "workflowEvent", label: "When a workflow run ends" },
];

export interface WhenFieldProps {
  idPrefix: string;
  when: WhenState;
  onChange: (when: WhenState) => void;
  /** Shown under the schedule or event once the person tried to save. */
  error?: string;
  /** App events: the connection the events arrive through. */
  connectionId: string;
  onConnectionChange: (connectionId: string) => void;
  actingIdentity: ActingIdentityKey;
  onActingIdentityChange: (value: ActingIdentityKey) => void;
  /** The stored trigger, for "Only from" lookups. */
  triggerId?: string;
  /** Rendered under the WHEN: e.g. "Workflows that already handle this". */
  children?: ReactNode;
}

export function WhenField({
  idPrefix,
  when,
  onChange,
  error,
  connectionId,
  onConnectionChange,
  actingIdentity,
  onActingIdentityChange,
  triggerId,
  children,
}: WhenFieldProps) {
  const queryClient = useQueryClient();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [connectEntry, setConnectEntry] = useState<CatalogEntrySummary | null>(null);
  const [connectOpen, setConnectOpen] = useState(false);
  const id = (name: string) => `${idPrefix}-${name}`;

  const integration = when.event ? integrationOf(when.event) : undefined;
  const resolvedRef = useDeclaredTriggerRef(integration?.integration, integration?.events ?? [], when.catalogRef);
  const entry = useCatalogEntry(when.kind === "integration" ? resolvedRef : undefined).data;

  const chooseCatalog = async (summary: CatalogEntrySummary) => {
    setPaletteOpen(false);
    let full: CatalogEntry | undefined;
    try {
      full = await fetchCatalogEntry(queryClient, summary.ref);
    } catch {
      full = undefined;
    }
    const events = ((full?.payloadSchema?.properties?.event?.enum ?? []) as unknown[]).map(String);
    onChange(withKind(when, "integration", triggerFromCatalog(summary, events, []), summary.ref));
    // Each app has its own accounts; the last app's does not carry over.
    onConnectionChange("");
  };

  const chooseKind = (kind: WhenKind) => {
    if (kind === "integration") {
      setPaletteOpen(true);
      return;
    }
    onChange(withKind(when, kind));
  };

  return (
    <div className="space-y-3" role="group" aria-label="When it runs" aria-invalid={!!error} aria-describedby={error ? id("error") : undefined}>
      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor={id("kind")} className="sr-only">
          What starts it
        </label>
        <select
          id={id("kind")}
          className={`${fieldClass} w-auto`}
          value={when.kind}
          onChange={(e) => chooseKind(e.target.value as WhenKind)}
        >
          {KIND_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
        {when.kind === "integration" && (
          <button
            type="button"
            onClick={() => setPaletteOpen(true)}
            className="rounded-sm text-xs font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            {integration?.integration ? "Choose another event" : "Choose the event"}
          </button>
        )}
      </div>

      {when.kind === "schedule" && (
        <ScheduleSentence
          idPrefix={id("when")}
          value={when.schedule}
          timezone={when.timezone}
          onChange={(patch) => onChange({ ...when, schedule: { ...when.schedule, ...patch } })}
          onTimezoneChange={(timezone) => onChange({ ...when, timezone })}
          error={undefined}
        />
      )}

      {when.kind === "webhook" && (
        <p className={hintClass}>
          It runs when something POSTs to its own URL. You get the URL and its token right after you create it.
        </p>
      )}

      {(when.kind === "integration" || when.kind === "workflowEvent") && when.event && (
        <div id={id("event")} tabIndex={-1} className="focus:outline-none">
          <TriggerSourceFields trigger={when.event} catalogRef={resolvedRef} onChange={(event) => onChange({ ...when, event })} />
        </div>
      )}

      {when.kind === "integration" && !integration?.integration && (
        <p className={hintClass}>Pick the app and the event that starts it: a pull request opened, a Slack message, an email.</p>
      )}

      {when.kind === "integration" && integration?.integration && (
        <div className="space-y-4">
          {entry ? (
            <div>
              <ConnectionPicker entry={entry} value={connectionId} onChange={onConnectionChange} onConnect={() => setConnectOpen(true)} />
              <p className={hintClass}>Events reach this automation through this account.</p>
            </div>
          ) : (
            <p className={hintClass}>Your default {integration.integration} account is used.</p>
          )}
          <ActingIdentityField value={actingIdentity} onChange={onActingIdentityChange} allowApp={integration.integration === "github"} />
          <OnlyFromControl
            integration={integration.integration}
            filter={when.event?.filter ?? ""}
            onChange={(filter) => onChange({ ...when, event: { ...when.event!, filter } })}
            connectionId={connectionId || undefined}
            triggerId={triggerId}
            classes={{ label: labelClass, input: fieldClass, hint: hintClass }}
          />
        </div>
      )}

      {error && (
        <p id={id("error")} className={errorTextClass} role="alert">
          {error}
        </p>
      )}

      {children}

      <StepPalette
        open={paletteOpen}
        initialKind="trigger"
        onClose={() => setPaletteOpen(false)}
        onChooseBuiltin={() => setPaletteOpen(false)}
        onChooseAction={() => setPaletteOpen(false)}
        onChooseTrigger={(summary) => void chooseCatalog(summary)}
        onChooseBuiltinTrigger={(source) => {
          setPaletteOpen(false);
          onChange(withKind(when, source === "workflow_event" ? "workflowEvent" : source));
        }}
        onConnect={(summary) => setConnectEntry(summary)}
      />
      <ConnectIntegrationDialog
        target={
          connectOpen && integration?.integration && resolvedRef
            ? {
                ref: resolvedRef,
                integrationId: integration.integration,
                displayName: entry?.summary.integration.displayName ?? integration.integration,
                icon: entry?.summary.integration.icon,
              }
            : connectEntry
              ? {
                  ref: connectEntry.ref,
                  integrationId: connectEntry.integration.id,
                  displayName: connectEntry.integration.displayName,
                  icon: connectEntry.integration.icon,
                }
              : null
        }
        onClose={() => {
          setConnectOpen(false);
          setConnectEntry(null);
        }}
        onConnected={(connection) => onConnectionChange(connection.id)}
      />
    </div>
  );
}
