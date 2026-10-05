// Copyright (c) 2025 Reliant Labs

/**
 * Edit one of the workflow's declared triggers (its `triggers:` entry; the
 * WHEN, research/INTEGRATIONS_V1_BRIEF.md §3a):
 *
 *   - Source: a schedule (the Automations presets), a webhook (optional HMAC),
 *     an integration event (events + match, from the catalog entry's events
 *     and attributes) or another workflow's outcome.
 *   - Filter: a raw CEL bool over `trigger`, with the server's validation
 *     findings beside it. Not offered for a schedule, which has no event.
 *   - Inputs: each workflow input ← a template over `trigger.payload`, with
 *     completion from the trigger's payload_schema.
 *   - Activations: the caller's rows activating it, and "Activate".
 *
 * Every edit is a whole-trigger update through WorkflowMutationContext, so
 * the definition the builder saves is the canonical one.
 */

import { useId, useMemo, useState } from "react";
import { AlertTriangle, Plus, Webhook, Zap } from "lucide-react";

import { ConfigurationPanel } from "../ConfigurationPanel";
import { CELInput } from "../CELInput";
import { CELCompletionProvider, useCELCompletionContext } from "../CELCompletionContext";
import { useWorkflowMutations } from "../WorkflowMutationContext";
import { IntegrationIcon } from "../palette/IntegrationIcon";
import { Section, SectionFields, SectionLabel } from "./primitives";
import { ScheduleFields } from "../../Automations/AutomationFormDialog";
import { formFromSchedule, scheduleFromForm, validateScheduleForm, type ScheduleFormState } from "../../Automations/scheduleForm";
import { HealthIndicator } from "../../Automations/AutomationRow";
import { automationHealth } from "../../../lib/automationHealth";
import { describeSchedule } from "../../../lib/cronText";
import {
  canFilter,
  findingFieldLabel,
  integrationOf,
  scheduleOf,
  sourceCase,
  triggerNameError,
  withFilter,
  withInput,
  withIntegration,
  withSchedule,
  withWorkflowEvent,
  workflowEventOf,
  type DeclaredTrigger,
  type TriggerFinding,
} from "../../../lib/declaredTriggers";
import type { Trigger } from "../../../api/trigger-grpc";
import type { JsonSchema } from "../../../lib/jsonSchema";
import { useCatalogEntry, useDeclaredTriggerRef } from "../../../hooks/connection-queries";
import type { Workflow } from "../../../types/workflow";

export interface DeclaredTriggerPanelProps {
  index: number;
  trigger: DeclaredTrigger;
  allTriggers: readonly DeclaredTrigger[];
  /** The workflow's inputs, for the mapping editor. */
  inputs: Workflow["inputs"];
  /** The catalog ref the trigger was added from, when known (for its payload schema and events). */
  catalogRef?: string;
  findings: TriggerFinding[];
  activations: Trigger[];
  isReadOnly: boolean;
  canActivate: boolean;
  /** Edited since the last save: activation reads the stored declaration. */
  unsaved: boolean;
  onClose: () => void;
  onActivate: () => void;
  onEditActivation: (trigger: Trigger) => void;
  bottomOffset?: number;
  topOffset?: number;
}

const OUTCOMES = ["finished", "failed", "blocked"] as const;

function findingsFor(findings: TriggerFinding[], prefix: string): TriggerFinding[] {
  return findings.filter((f) => f.field === prefix || f.field.startsWith(`${prefix}.`));
}

function FindingList({ findings }: { findings: TriggerFinding[] }) {
  if (findings.length === 0) return null;
  return (
    <ul className="space-y-1" aria-label="Problems">
      {findings.map((finding, i) => (
        <li key={i} className="flex items-start gap-1.5 text-xs text-warning-ink">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" aria-hidden />
          <span>
            {finding.field && <span className="font-medium">{findingFieldLabel(finding.field)}: </span>}
            {finding.message}
            {finding.suggestion && <span className="block text-muted-foreground">{finding.suggestion}</span>}
          </span>
        </li>
      ))}
    </ul>
  );
}

export function DeclaredTriggerPanel(props: DeclaredTriggerPanelProps) {
  const { index, trigger, allTriggers, inputs, findings, isReadOnly, onClose, bottomOffset, topOffset } = props;
  const mutations = useWorkflowMutations();
  const ids = useId();
  const kind = sourceCase(trigger);
  const integration = integrationOf(trigger);
  // The payload schema: from the catalog ref the trigger was added from, or
  // the integration's first trigger matching its events.
  const catalogRef = useDeclaredTriggerRef(integration?.integration, integration?.events ?? [], props.catalogRef);
  const entryQuery = useCatalogEntry(catalogRef);
  const payloadSchema = entryQuery.data?.payloadSchema;

  const [nameDraft, setNameDraft] = useState(trigger.name ?? "");
  const nameError = triggerNameError(nameDraft, allTriggers, index);

  const update = (next: DeclaredTrigger) => mutations.updateTrigger(index, next);

  const title = trigger.name || "Trigger";
  const icon =
    kind === "integration" ? (
      <IntegrationIcon hint={integration?.integration} size="sm" className="border-0 bg-transparent" />
    ) : kind === "webhook" ? (
      <Webhook />
    ) : (
      <Zap />
    );

  return (
    <ConfigurationPanel
      title={title}
      subtitle="Declared trigger"
      subtitleMono={false}
      icon={icon}
      onClose={onClose}
      onDelete={isReadOnly ? undefined : () => mutations.removeTrigger(index)}
      deleteLabel="Remove trigger"
      bottomOffset={bottomOffset}
      topOffset={topOffset}
    >
      <TriggerPayloadCompletion schema={payloadSchema}>
        <Section>
          <div className="cpv2-field-label">
            <label htmlFor={`${ids}-name`}>Name</label>
          </div>
          <input
            id={`${ids}-name`}
            value={nameDraft}
            disabled={isReadOnly}
            onChange={(e) => setNameDraft(e.target.value)}
            onBlur={() => {
              if (!nameError && nameDraft !== trigger.name) update({ ...trigger, name: nameDraft } as DeclaredTrigger);
            }}
            aria-invalid={!!nameError}
            aria-describedby={nameError ? `${ids}-name-error` : `${ids}-name-hint`}
            className="cpv2-field-input w-full font-mono"
          />
          {nameError ? (
            <p id={`${ids}-name-error`} className="cpv2-field-hint !text-destructive-ink">{nameError}</p>
          ) : (
            <p id={`${ids}-name-hint`} className="cpv2-field-hint">
              Activations refer to this name; renaming breaks them until they are re-activated.
            </p>
          )}
          <div className="cpv2-field-label mt-3">
            <label htmlFor={`${ids}-desc`}>Description</label>
          </div>
          <input
            id={`${ids}-desc`}
            defaultValue={trigger.description ?? ""}
            disabled={isReadOnly}
            onBlur={(e) => {
              if (e.target.value !== (trigger.description ?? "")) update({ ...trigger, description: e.target.value } as DeclaredTrigger);
            }}
            placeholder="What this trigger is for"
            className="cpv2-field-input w-full"
          />
          <FindingList findings={findings.filter((f) => f.field === "" || f.field === "name" || f.field === "source")} />
        </Section>

        {kind === "schedule" && <ScheduleSection trigger={trigger} onChange={update} disabled={isReadOnly} findings={findingsFor(findings, "schedule")} />}
        {kind === "webhook" && (
          <Section>
            <SectionLabel>Webhook</SectionLabel>
            <p className="cpv2-field-hint !mt-0">
              Fires on a POST to this trigger's URL. Each activation gets its own URL and token, shown when you activate it.
            </p>
            <FindingList findings={findingsFor(findings, "webhook")} />
          </Section>
        )}
        {kind === "integration" && integration && (
          <IntegrationSection
            trigger={trigger}
            source={integration}
            catalogRef={catalogRef}
            onChange={update}
            disabled={isReadOnly}
            findings={findingsFor(findings, "integration")}
          />
        )}
        {kind === "workflowEvent" && <WorkflowEventSection trigger={trigger} onChange={update} disabled={isReadOnly} findings={findingsFor(findings, "workflow_event")} />}

        {canFilter(trigger) && (
          <Section>
            <SectionLabel>Filter</SectionLabel>
            <CELInput
              id={`${ids}-filter`}
              value={trigger.filter ?? ""}
              onChange={(value) => update(withFilter(trigger, value))}
              disabled={isReadOnly}
              pureExpression
              hideCELHint
              placeholder="trigger.payload.data.issue.user.login != 'dependabot[bot]'"
            />
            <p className="cpv2-field-hint">
              A CEL expression over <code className="font-mono">trigger</code>. Only events where it is true start a run; the others are
              recorded as skipped. Leave empty to run on every event.
            </p>
            <FindingList findings={findingsFor(findings, "filter")} />
          </Section>
        )}

        <InputsSection trigger={trigger} inputs={inputs} onChange={update} disabled={isReadOnly} findings={findingsFor(findings, "inputs")} />

        <ActivationsSection {...props} />
      </TriggerPayloadCompletion>
    </ConfigurationPanel>
  );
}

/** Adds the trigger's payload schema to the surrounding CEL completion. */
function TriggerPayloadCompletion({ schema, children }: { schema?: JsonSchema; children: React.ReactNode }) {
  const outer = useCELCompletionContext();
  const value = useMemo(
    () => ({ ...(outer ?? { nodeIds: [], nodeTypeMap: {}, inputParams: {} }), triggerPayloadSchema: schema ?? outer?.triggerPayloadSchema }),
    [outer, schema],
  );
  return <CELCompletionProvider value={value}>{children}</CELCompletionProvider>;
}

function ScheduleSection({ trigger, onChange, disabled, findings }: { trigger: DeclaredTrigger; onChange: (t: DeclaredTrigger) => void; disabled: boolean; findings: TriggerFinding[] }) {
  const ids = useId();
  const schedule = scheduleOf(trigger)!;
  const [form, setForm] = useState<ScheduleFormState>(() => formFromSchedule({ cron: schedule.cron, interval: schedule.interval }));
  const [timezone, setTimezone] = useState(schedule.timezone ?? "");
  const error = validateScheduleForm(form);
  const apply = (nextForm: ScheduleFormState, nextZone: string) => {
    if (validateScheduleForm(nextForm)) return;
    const wire = scheduleFromForm(nextForm);
    onChange(withSchedule(trigger, { cron: wire.cron, interval: wire.interval, timezone: nextZone.trim() }));
  };
  return (
    <Section>
      <SectionLabel>Schedule</SectionLabel>
      <fieldset disabled={disabled} className="space-y-3">
        <ScheduleFields
          fieldId={(name) => `${ids}-${name}`}
          schedule={form}
          error={error ?? undefined}
          onChange={(patch) => {
            const next = { ...form, ...patch };
            setForm(next);
            apply(next, timezone);
          }}
        />
        <div>
          <label htmlFor={`${ids}-tz`} className="cpv2-field-label">Time zone</label>
          <input
            id={`${ids}-tz`}
            value={timezone}
            onChange={(e) => setTimezone(e.target.value)}
            onBlur={() => apply(form, timezone)}
            placeholder="UTC"
            className="cpv2-field-input w-full"
          />
        </div>
        {!error && (
          <p className="cpv2-field-hint !mt-0">
            {describeSchedule({ ...scheduleFromForm(form), timezone: timezone.trim() || "UTC" })}
          </p>
        )}
      </fieldset>
      <FindingList findings={findings} />
    </Section>
  );
}

function IntegrationSection({
  trigger,
  source,
  catalogRef,
  onChange,
  disabled,
  findings,
}: {
  trigger: DeclaredTrigger;
  source: NonNullable<ReturnType<typeof integrationOf>>;
  catalogRef?: string;
  onChange: (t: DeclaredTrigger) => void;
  disabled: boolean;
  findings: TriggerFinding[];
}) {
  const ids = useId();
  const entry = useCatalogEntry(catalogRef).data;
  const eventOptions = (entry?.payloadSchema?.properties?.event?.enum ?? []) as string[];
  const attributeProps = entry?.payloadSchema?.properties?.attributes?.properties ?? {};
  const attributes = Object.entries(attributeProps);
  const matchKeys = [...new Set([...attributes.map(([name]) => name), ...Object.keys(source.match)])];
  const [eventsText, setEventsText] = useState(source.events.join(", "));

  return (
    <Section>
      <SectionLabel>{entry ? `${entry.summary.integration.displayName}: ${entry.summary.displayName}` : `Integration: ${source.integration}`}</SectionLabel>
      <SectionFields>
        {eventOptions.length > 0 ? (
          <fieldset disabled={disabled}>
            <legend className="cpv2-field-label">Events</legend>
            <div className="flex flex-wrap gap-1.5">
              {eventOptions.map((event) => {
                const on = source.events.includes(event);
                return (
                  <button
                    key={event}
                    type="button"
                    aria-pressed={on}
                    onClick={() =>
                      onChange(withIntegration(trigger, { ...source, events: on ? source.events.filter((e) => e !== event) : [...source.events, event] }))
                    }
                    className={
                      on
                        ? "rounded-full border border-primary bg-primary px-2 py-0.5 font-mono text-2xs text-primary-foreground"
                        : "rounded-full border border-border px-2 py-0.5 font-mono text-2xs text-muted-foreground hover:bg-muted hover:text-foreground"
                    }
                  >
                    {event}
                  </button>
                );
              })}
            </div>
          </fieldset>
        ) : (
          <div>
            <label htmlFor={`${ids}-events`} className="cpv2-field-label">Events</label>
            <input
              id={`${ids}-events`}
              value={eventsText}
              disabled={disabled}
              onChange={(e) => setEventsText(e.target.value)}
              onBlur={() => onChange(withIntegration(trigger, { ...source, events: eventsText.split(",").map((e) => e.trim()).filter(Boolean) }))}
              placeholder="issues.opened, issues.*"
              className="cpv2-field-input w-full font-mono"
            />
            <p className="cpv2-field-hint">Event types as {source.integration} names them; a trailing .* matches every action.</p>
          </div>
        )}

        {matchKeys.length > 0 && (
          <fieldset disabled={disabled} className="space-y-2">
            <legend className="cpv2-field-label">Only when</legend>
            {matchKeys.map((name) => {
              const schema = attributeProps[name] as JsonSchema | undefined;
              const example = schema?.examples?.[0];
              return (
                <div key={name}>
                  <label htmlFor={`${ids}-match-${name}`} className="mb-1 block font-mono text-2xs text-muted-foreground">{name}</label>
                  <input
                    id={`${ids}-match-${name}`}
                    defaultValue={source.match[name] ?? ""}
                    onBlur={(e) => onChange(withIntegration(trigger, { ...source, match: { ...source.match, [name]: e.target.value.trim() } }))}
                    placeholder={typeof example === "string" ? example : "any"}
                    className="cpv2-field-input w-full font-mono"
                  />
                  {schema?.description && <p className="cpv2-field-hint">{schema.description}</p>}
                </div>
              );
            })}
          </fieldset>
        )}
      </SectionFields>
      <FindingList findings={findings} />
    </Section>
  );
}

function WorkflowEventSection({ trigger, onChange, disabled, findings }: { trigger: DeclaredTrigger; onChange: (t: DeclaredTrigger) => void; disabled: boolean; findings: TriggerFinding[] }) {
  const ids = useId();
  const event = workflowEventOf(trigger)!;
  const [workflowsText, setWorkflowsText] = useState(event.workflows.join(", "));
  return (
    <Section>
      <SectionLabel>When a workflow finishes</SectionLabel>
      <SectionFields>
        <div>
          <label htmlFor={`${ids}-wf`} className="cpv2-field-label">Workflows</label>
          <input
            id={`${ids}-wf`}
            value={workflowsText}
            disabled={disabled}
            onChange={(e) => setWorkflowsText(e.target.value)}
            onBlur={() => onChange(withWorkflowEvent(trigger, { ...event, workflows: workflowsText.split(",").map((w) => w.trim()).filter(Boolean) }))}
            placeholder="Any of your workflows"
            className="cpv2-field-input w-full font-mono"
          />
          <p className="cpv2-field-hint">Workflow names or builtin://refs, comma-separated. Empty matches any.</p>
        </div>
        <fieldset disabled={disabled}>
          <legend className="cpv2-field-label">Outcomes</legend>
          <div className="flex gap-1.5">
            {OUTCOMES.map((outcome) => {
              const on = event.outcomes.includes(outcome);
              return (
                <button
                  key={outcome}
                  type="button"
                  aria-pressed={on}
                  onClick={() => onChange(withWorkflowEvent(trigger, { ...event, outcomes: on ? event.outcomes.filter((o) => o !== outcome) : [...event.outcomes, outcome] }))}
                  className={
                    on
                      ? "rounded-full border border-primary bg-primary px-2.5 py-0.5 text-xs text-primary-foreground"
                      : "rounded-full border border-border px-2.5 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
                  }
                >
                  {outcome}
                </button>
              );
            })}
          </div>
          <p className="cpv2-field-hint">None selected matches all three.</p>
        </fieldset>
      </SectionFields>
      <FindingList findings={findings} />
    </Section>
  );
}

function InputsSection({ trigger, inputs, onChange, disabled, findings }: { trigger: DeclaredTrigger; inputs: Workflow["inputs"]; onChange: (t: DeclaredTrigger) => void; disabled: boolean; findings: TriggerFinding[] }) {
  const ids = useId();
  const mapped = (trigger.inputs ?? {}) as Record<string, string>;
  const declared = Object.keys(inputs ?? {});
  // A mapping for an input the workflow no longer declares stays visible, so it can be removed.
  const names = [...new Set([...declared, ...Object.keys(mapped)])];
  return (
    <Section>
      <SectionLabel>Inputs from the event</SectionLabel>
      {names.length === 0 ? (
        <p className="cpv2-field-hint !mt-0 italic">This workflow declares no inputs to fill.</p>
      ) : (
        <SectionFields>
          {names.map((name) => (
            <div key={name}>
              <div className="cpv2-field-label">
                <label htmlFor={`${ids}-in-${name}`} className="font-mono">{name}</label>
                {!declared.includes(name) && <span className="text-2xs text-warning-ink">not a declared input</span>}
              </div>
              <CELInput
                id={`${ids}-in-${name}`}
                value={mapped[name] ?? ""}
                onChange={(value) => onChange(withInput(trigger, name, value))}
                disabled={disabled}
                hideCELHint
                placeholder="{{ trigger.payload.data… }}"
              />
            </div>
          ))}
          <p className="cpv2-field-hint">
            Set from each event; an activation can't override a mapped input. Leave empty to let the activation (or the input's default) provide it.
          </p>
        </SectionFields>
      )}
      <FindingList findings={findings} />
    </Section>
  );
}

function ActivationsSection({ trigger, activations, canActivate, unsaved, onActivate, onEditActivation }: DeclaredTriggerPanelProps) {
  return (
    <Section>
      <SectionLabel>Activations</SectionLabel>
      {activations.length === 0 ? (
        <p className="cpv2-field-hint !mt-0">
          Declaring a trigger doesn't fire anything. Activate it to choose the project, machine and connection its runs use.
        </p>
      ) : (
        <ul className="divide-y divide-border/60 rounded-md border border-border/60 bg-background" aria-label={`Activations of ${trigger.name}`}>
          {activations.map((activation) => (
            <li key={activation.id}>
              <button
                type="button"
                onClick={() => onEditActivation(activation)}
                className="flex w-full items-center gap-2 px-2.5 py-2 text-left text-xs hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate font-medium text-foreground">{activation.name}</span>
                  <span className="block truncate text-muted-foreground">
                    {activation.projectName ?? "Deleted project"} · {activation.daemonName ?? "machine"}
                  </span>
                </span>
                <HealthIndicator health={automationHealth(activation)} />
              </button>
            </li>
          ))}
        </ul>
      )}
      <button
        type="button"
        onClick={onActivate}
        disabled={!canActivate || unsaved}
        className="mt-2 inline-flex items-center gap-1 rounded-md bg-primary px-2.5 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
      >
        <Plus className="h-3.5 w-3.5" aria-hidden />
        {activations.length === 0 ? "Activate" : "Activate again"}
      </button>
      {(unsaved || !canActivate) && (
        <p className="cpv2-field-hint">
          {unsaved ? "Save the workflow first: an activation uses the saved trigger." : "Save the workflow to activate its triggers."}
        </p>
      )}
    </Section>
  );
}
