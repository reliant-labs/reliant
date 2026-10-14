// Copyright (c) 2025 Reliant Labs

/**
 * Enable a workflow: turn on every trigger it declares, in one step.
 *
 * A workflow's YAML says WHEN it runs; nothing fires until the person it runs
 * for says where and as whom. That is this dialog, and it is the only way a
 * workflow's triggers start (no agent tool does it). The choices are made
 * once and apply to all of the workflow's triggers:
 *
 *   project · workspace · machine (or "No machine") · the connected account
 *   each integration needs · the workflow's own inputs ("activation params") ·
 *   the prompt for a trigger whose author wrote none · notify
 *
 * A trigger that needs something different (another machine, another
 * account, its own prompt) is overridden under "Advanced", one disclosure per
 * trigger, so the common case is one screen and the odd one is still possible.
 *
 * Enabling is all or nothing. When it is refused the dialog stays open on the
 * same choices and says which trigger could not be enabled and why, in the
 * server's words ("connect GitHub first"), and that the others were not turned
 * on either. The one outcome it cannot hide is a trigger that could not be
 * switched back off: that is shown as an error with where to turn it off.
 *
 * A webhook's URL and token come back once, with the success; they are shown
 * here with copy and cannot be fetched again (only rotated).
 */

import { useEffect, useId, useMemo, useState, type FormEvent } from "react";
import { AlertTriangle, ChevronDown, ChevronRight, CircleCheck, CircleX, MinusCircle } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";

import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { CardInset } from "../forge-ui/card";
import { Toggle } from "../ui/Toggle";
import { useProjectStore } from "@/store/projectStore";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useProjectDaemonInstalls, triggerKeys } from "@/hooks/trigger-queries";
import {
  triggerErrorMessage,
  triggerGrpc,
  type EnableWorkflowInput,
  type EnableWorkflowResult,
  type WorkflowTriggerOverrideInput,
  type WorkflowTriggerResult,
} from "@/api/trigger-grpc";
import { RunWorkflowForm, type RunWorkflowFormStatus } from "../workflow/run/RunWorkflowForm";
import type { RunWorkflowValue } from "../workflow/run/runWorkflowValues";
import { errorTextClass, fieldClass, hintClass, labelClass, textareaClass } from "../workflow/run/runFormStyles";
import { ConnectionPicker } from "../workflow/connections/ConnectionPicker";
import { ConnectIntegrationDialog } from "../workflow/connections/ConnectIntegrationDialog";
import { useCatalogEntry, useDeclaredTriggerRef, useDeclaredTriggerRefState, useIntegrationEventNaming } from "@/hooks/connection-queries";
import { describeDeclaredSource, integrationOf, promptOf, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { describeSchedule } from "@/lib/cronText";
import { buildDaemonChoices, defaultDaemonId, NO_MACHINE } from "./daemonChoices";
import { WebhookCredentialPanel } from "./ActivateTriggerDialog";
import { outcomeLabel, planEnable, refusalHeadline, summarizeEnable, triggersNeedingPrompt } from "./enableWorkflow";

export interface EnableWorkflowDialogProps {
  open: boolean;
  onClose: () => void;
  /** The stored workflow ref the triggers are declared in. */
  workflowRef: string;
  workflowTitle?: string;
  /** Every trigger the workflow declares, in declaration order. */
  declared: readonly DeclaredTrigger[];
  defaultProjectId?: string;
  /** Called once every trigger is running. */
  onEnabled?: (result: EnableWorkflowResult) => void;
}

export function EnableWorkflowDialog(props: EnableWorkflowDialogProps) {
  if (!props.open) return null;
  return <EnableBody {...props} />;
}

interface Override {
  machine?: string;
  connectionId?: string;
  message?: string;
}

function EnableBody({ onClose, workflowRef, workflowTitle, declared, defaultProjectId, onEnabled }: EnableWorkflowDialogProps) {
  const ids = useId();
  const fieldId = (name: string) => `${ids}-${name}`;
  const queryClient = useQueryClient();
  const projects = useProjectStore((s) => s.projects);
  const currentProjectId = useProjectStore((s) => s.currentProject?.id);
  const naming = useIntegrationEventNaming();

  const plan = useMemo(() => planEnable(declared), [declared]);
  const needPrompt = useMemo(() => triggersNeedingPrompt(declared), [declared]);

  const [projectId, setProjectId] = useState(defaultProjectId ?? currentProjectId ?? projects[0]?.id ?? "");
  const [machine, setMachine] = useState("");
  const [machineChosen, setMachineChosen] = useState(false);
  const [connections, setConnections] = useState<Record<string, string>>({});
  const [inputs, setInputs] = useState<RunWorkflowValue>({ presets: {}, params: {} });
  const [inputsStatus, setInputsStatus] = useState<RunWorkflowFormStatus | null>(null);
  const [message, setMessage] = useState("");
  const [notify, setNotify] = useState(false);
  const [overrides, setOverrides] = useState<Record<string, Override>>({});
  const [advancedOpen, setAdvancedOpen] = useState<Record<string, boolean>>({});
  const [connectTarget, setConnectTarget] = useState<string | null>(null);
  const [attempted, setAttempted] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<EnableWorkflowResult | null>(null);

  const { daemons, loading: daemonsLoading } = useDaemonStatus();
  const installs = useProjectDaemonInstalls();
  const choices = useMemo(() => buildDaemonChoices(daemons, installs.data ?? [], projectId), [daemons, installs.data, projectId]);
  useEffect(() => {
    if (machineChosen || daemonsLoading || installs.isLoading) return;
    setMachine(defaultDaemonId(choices) ?? "");
  }, [choices, machineChosen, daemonsLoading, installs.isLoading]);

  const verdict = result ? summarizeEnable(result) : null;
  if (verdict?.kind === "enabled") {
    return <EnabledView workflowTitle={workflowTitle || workflowRef} result={result!} onClose={onClose} />;
  }

  const errors: Record<string, string> = {};
  if (!projectId) errors.project = "Choose a project.";
  if (!machine) errors.machine = daemons.length === 0 ? "Connect a machine first, or choose No machine." : "Choose where the runs execute.";
  if (inputsStatus?.loading) errors.inputs = "Wait for this workflow's inputs to load.";
  else if (inputsStatus?.missingRequired.length) errors.inputs = `Fill in: ${inputsStatus.missingRequired.join(", ")}.`;
  const unwritten = message.trim() ? [] : needPrompt.filter((name) => !overrides[name]?.message?.trim());
  if (unwritten.length > 0) {
    errors.message = `Write the prompt each run starts from: ${unwritten.map((n) => `“${n}”`).join(", ")} ${unwritten.length === 1 ? "has" : "have"} none of its own.`;
  }

  const patchOverride = (trigger: string, patch: Override) =>
    setOverrides((prev) => ({ ...prev, [trigger]: { ...prev[trigger], ...patch } }));

  const buildInput = (): EnableWorkflowInput => {
    const overrideInputs: WorkflowTriggerOverrideInput[] = [];
    for (const [trigger, o] of Object.entries(overrides)) {
      const entry: WorkflowTriggerOverrideInput = { trigger };
      if (o.machine) {
        entry.noMachine = o.machine === NO_MACHINE;
        entry.daemonId = o.machine === NO_MACHINE ? "" : o.machine;
      }
      if (o.connectionId) entry.connectionId = o.connectionId;
      if (o.message?.trim()) entry.message = o.message.trim();
      if (entry.noMachine !== undefined || entry.connectionId || entry.message) overrideInputs.push(entry);
    }
    return {
      workflow: workflowRef,
      projectId,
      worktreeId: inputs.worktreeId,
      daemonId: machine === NO_MACHINE ? "" : machine,
      noMachine: machine === NO_MACHINE,
      connectionIds: Object.fromEntries(Object.entries(connections).filter(([, id]) => id)),
      params: inputs.params,
      presets: inputs.presets,
      notifyOnComplete: notify,
      message: message.trim(),
      namePrefix: workflowTitle || undefined,
      overrides: overrideInputs,
    };
  };

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setAttempted(true);
    setError(null);
    if (Object.keys(errors).length > 0) return;
    setSaving(true);
    try {
      const outcome = await triggerGrpc.enableWorkflow(buildInput());
      void queryClient.invalidateQueries({ queryKey: triggerKeys.lists() });
      setResult(outcome);
      if (outcome.enabled) onEnabled?.(outcome);
    } catch (err) {
      setError(triggerErrorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  const show = (key: string) => attempted && errors[key];
  const title = workflowTitle || workflowRef;

  return (
    <Modal isOpen onClose={onClose} size="lg" title={`Enable ${title}`}>
      <form onSubmit={onSubmit} noValidate className="space-y-5" aria-label={`Enable ${title}`}>
        <CardInset className="text-sm">
          <p className="text-foreground">
            Turns on {declared.length === 1 ? "the one trigger" : `all ${declared.length} triggers`} this workflow declares. Until you do, nothing runs on its own.
          </p>
          <ul aria-label="Triggers this will turn on" className="mt-2 space-y-1.5">
            {declared.map((trigger) => (
              <li key={trigger.name} className="flex flex-wrap items-baseline gap-x-2 text-xs">
                <span className="font-mono text-foreground">{trigger.name}</span>
                <span className="text-muted-foreground">{describeDeclaredSource(trigger, describeSchedule, naming)}</span>
              </li>
            ))}
          </ul>
        </CardInset>

        {verdict && <RefusalNotice verdict={verdict} />}

        <div className="grid gap-4 sm:grid-cols-2">
          <div>
            <label htmlFor={fieldId("project")} className={labelClass}>Project</label>
            <select
              id={fieldId("project")}
              className={fieldClass}
              value={projectId}
              onChange={(e) => {
                setProjectId(e.target.value);
                setInputs((prev) => ({ ...prev, worktreeId: undefined }));
              }}
            >
              {projects.length === 0 && <option value="">No projects</option>}
              {projects.map((project) => (
                <option key={project.id} value={project.id}>{project.name}</option>
              ))}
            </select>
            {show("project") && <p className={errorTextClass}>{errors.project}</p>}
          </div>
          <div>
            <label htmlFor={fieldId("machine")} className={labelClass}>Runs on</label>
            <MachineSelect
              id={fieldId("machine")}
              value={machine}
              loading={daemonsLoading}
              choices={choices}
              onChange={(value) => {
                setMachine(value);
                setMachineChosen(true);
              }}
              invalid={!!show("machine")}
            />
            <p className={hintClass}>
              {machine === NO_MACHINE
                ? "Runs use only web, integration and planning tools; nothing touches files."
                : "Every tool call in each run executes on this machine."}
            </p>
            {show("machine") && <p className={errorTextClass}>{errors.machine}</p>}
          </div>
        </div>

        {plan.integrations.map((integration) => (
          <IntegrationConnection
            key={integration}
            integration={integration}
            usedBy={plan.triggersByIntegration[integration] ?? []}
            declared={declared}
            value={connections[integration] ?? ""}
            onChange={(id) => setConnections((prev) => ({ ...prev, [integration]: id }))}
            onConnect={() => setConnectTarget(integration)}
          />
        ))}

        <div>
          <label htmlFor={fieldId("message")} className={labelClass}>Prompt</label>
          <textarea
            id={fieldId("message")}
            className={textareaClass}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder={needPrompt.length === 0 ? "Optional: every trigger already has a prompt in the workflow." : "Each run starts from this message. Nobody will be watching, so say everything the agent needs."}
            aria-invalid={!!show("message")}
          />
          <p className={hintClass}>
            {needPrompt.length === 0
              ? "Every trigger brings its own prompt, and these words are not used."
              : needPrompt.length === declared.length
                ? "Used by every trigger, since none of them declares a prompt."
                : `Used by ${needPrompt.map((n) => `“${n}”`).join(", ")}, which declare no prompt. The others keep theirs.`}
          </p>
          {show("message") && <p className={errorTextClass} role="alert">{errors.message}</p>}
        </div>

        <WorkflowInputs
          projectId={projectId}
          workflowRef={workflowRef}
          declared={declared}
          value={inputs}
          onChange={setInputs}
          onStatusChange={setInputsStatus}
          showValidation={attempted}
        />
        {show("inputs") && !inputsStatus?.missingRequired.length && <p className={errorTextClass} role="alert">{errors.inputs}</p>}

        <div className="flex items-center gap-2">
          <Toggle checked={notify} onChange={setNotify} srLabel="Notify me when a run completes" />
          <span className="text-sm text-foreground" aria-hidden>Notify me when a run completes</span>
        </div>

        {declared.length > 1 && (
          <PerTriggerOverrides
            declared={declared}
            overrides={overrides}
            open={advancedOpen}
            onToggle={(name) => setAdvancedOpen((prev) => ({ ...prev, [name]: !prev[name] }))}
            onPatch={patchOverride}
            choices={choices}
            sharedMessage={message}
            needPrompt={needPrompt}
            sharedConnections={connections}
            onConnect={(integration) => setConnectTarget(integration)}
          />
        )}

        {error && (
          <p role="alert" className="rounded-md border border-danger-border bg-danger-surface px-3 py-2 text-sm text-danger-ink">
            {error}
          </p>
        )}

        <div className="flex justify-end gap-2 border-t border-border pt-4">
          <Button type="button" variant="outline" onClick={onClose}>{verdict?.kind === "partial" ? "Close" : "Cancel"}</Button>
          <Button type="submit" variant="primary" loading={saving}>{verdict ? "Try again" : "Enable"}</Button>
        </div>
      </form>

      <ConnectFor
        integration={connectTarget}
        declared={declared}
        onClose={() => setConnectTarget(null)}
        onConnected={(integration, id) => setConnections((prev) => ({ ...prev, [integration]: id }))}
      />
    </Modal>
  );
}

function MachineSelect({
  id,
  value,
  loading,
  choices,
  onChange,
  invalid,
  inheritLabel,
}: {
  id: string;
  value: string;
  loading: boolean;
  choices: ReturnType<typeof buildDaemonChoices>;
  onChange: (value: string) => void;
  invalid?: boolean;
  /** Offered as the empty option (an override that follows the shared choice). */
  inheritLabel?: string;
}) {
  return (
    <select id={id} className={fieldClass} value={value} onChange={(e) => onChange(e.target.value)} aria-invalid={invalid}>
      <option value="">{inheritLabel ?? (loading ? "Loading machines…" : "Choose a machine")}</option>
      {choices.map((choice) => (
        <option key={choice.daemonId} value={choice.daemonId} disabled={!choice.eligible}>
          {choice.label} ({choice.statusLabel}{choice.ineligibleReason ? `, ${choice.ineligibleReason}` : ""})
        </option>
      ))}
      <option value={NO_MACHINE}>No machine (server tools only)</option>
    </select>
  );
}

/** The connected account an integration's triggers listen through. */
function IntegrationConnection({
  integration,
  usedBy,
  declared,
  value,
  onChange,
  onConnect,
}: {
  integration: string;
  usedBy: readonly string[];
  declared: readonly DeclaredTrigger[];
  value: string;
  onChange: (connectionId: string) => void;
  onConnect: () => void;
}) {
  const { entry, loading } = useIntegrationEntry(integration, declared);
  const who = usedBy.map((n) => `“${n}”`).join(", ");
  if (!entry) {
    return (
      <div>
        <span className={labelClass}>Connection · {integration}</span>
        <p className={hintClass} aria-live="polite">
          {loading
            ? `Loading ${integration}…`
            : `Couldn't look up how ${integration} connects. ${who} will use your default ${integration} connection; if you have none, enabling will say so.`}
        </p>
      </div>
    );
  }
  return (
    <div>
      <ConnectionPicker entry={entry} value={value} onChange={onChange} onConnect={onConnect} />
      <p className={hintClass}>Events reach {who} only through this account.</p>
    </div>
  );
}

/** The catalog entry for an integration's trigger, found the way the single-trigger dialog finds it. */
function useIntegrationEntry(integration: string, declared: readonly DeclaredTrigger[]) {
  const events = declared.flatMap((t) => {
    const source = integrationOf(t);
    return source?.integration === integration ? source.events : [];
  });
  const { ref, loading: searching } = useDeclaredTriggerRefState(integration, events);
  const query = useCatalogEntry(ref);
  return { entry: query.data, loading: searching || (!!ref && query.isLoading) };
}

function ConnectFor({
  integration,
  declared,
  onClose,
  onConnected,
}: {
  integration: string | null;
  declared: readonly DeclaredTrigger[];
  onClose: () => void;
  onConnected: (integration: string, connectionId: string) => void;
}) {
  const events = declared.flatMap((t) => {
    const source = integrationOf(t);
    return source?.integration === integration ? source.events : [];
  });
  const ref = useDeclaredTriggerRef(integration ?? undefined, events);
  const entry = useCatalogEntry(ref).data;
  return (
    <ConnectIntegrationDialog
      target={integration && ref ? { ref, integrationId: integration, displayName: entry?.summary.integration.displayName ?? integration, icon: entry?.summary.integration.icon } : null}
      onClose={onClose}
      onConnected={(connection) => integration && onConnected(integration, connection.id)}
    />
  );
}

/**
 * The workflow's own inputs: the "activation params". The inputs a trigger
 * maps from its event are not asked for: the event always wins over a param of
 * that name. If a workflow's triggers map different inputs, only the ones every
 * trigger maps are hidden here, and the rest are asked for once and dropped
 * per trigger by the server.
 */
function WorkflowInputs({
  projectId,
  workflowRef,
  declared,
  value,
  onChange,
  onStatusChange,
  showValidation,
}: {
  projectId: string;
  workflowRef: string;
  declared: readonly DeclaredTrigger[];
  value: RunWorkflowValue;
  onChange: (value: RunWorkflowValue) => void;
  onStatusChange: (status: RunWorkflowFormStatus) => void;
  showValidation: boolean;
}) {
  const everywhereMapped = useMemo(() => {
    const maps = declared.map((t) => new Set(Object.keys(t.inputs ?? {})));
    if (maps.length === 0) return [];
    return [...maps[0]].filter((name) => maps.every((m) => m.has(name)));
  }, [declared]);
  if (!projectId) return null;
  return (
    <RunWorkflowForm
      projectId={projectId}
      workflowRef={workflowRef}
      value={value}
      onChange={onChange}
      onStatusChange={onStatusChange}
      showValidation={showValidation}
      applyDefaultPresets
      excludeInputs={everywhereMapped}
    />
  );
}

function PerTriggerOverrides({
  declared,
  overrides,
  open,
  onToggle,
  onPatch,
  choices,
  sharedMessage,
  needPrompt,
  sharedConnections,
  onConnect,
}: {
  declared: readonly DeclaredTrigger[];
  overrides: Record<string, Override>;
  open: Record<string, boolean>;
  onToggle: (name: string) => void;
  onPatch: (trigger: string, patch: Override) => void;
  choices: ReturnType<typeof buildDaemonChoices>;
  sharedMessage: string;
  needPrompt: readonly string[];
  sharedConnections: Record<string, string>;
  onConnect: (integration: string) => void;
}) {
  const ids = useId();
  return (
    <fieldset className="space-y-2">
      <legend className={labelClass}>Advanced: per trigger</legend>
      <p className={hintClass}>Everything above applies to every trigger. Change it here for one of them.</p>
      <ul className="space-y-2">
        {declared.map((trigger) => {
          const name = trigger.name ?? "";
          const o = overrides[name] ?? {};
          const expanded = !!open[name];
          const changed = !!(o.machine || o.connectionId || o.message?.trim());
          const integration = integrationOf(trigger)?.integration;
          const panel = `${ids}-${name}`;
          return (
            <li key={name}>
              <CardInset padding="none">
                <button
                  type="button"
                  className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                  aria-expanded={expanded}
                  aria-controls={panel}
                  onClick={() => onToggle(name)}
                >
                  {expanded ? <ChevronDown className="h-4 w-4 shrink-0" aria-hidden /> : <ChevronRight className="h-4 w-4 shrink-0" aria-hidden />}
                  <span className="font-mono text-foreground">{name}</span>
                  {changed && <span className="ml-auto text-xs text-muted-foreground">customised</span>}
                </button>
                {expanded && (
                  <div id={panel} className="space-y-3 border-t border-border/60 px-3 py-3">
                    <div>
                      <label htmlFor={`${panel}-machine`} className={labelClass}>Runs on</label>
                      <MachineSelect
                        id={`${panel}-machine`}
                        value={o.machine ?? ""}
                        loading={false}
                        choices={choices}
                        onChange={(machine) => onPatch(name, { machine })}
                        inheritLabel="Same as above"
                      />
                    </div>
                    {integration && (
                      <div>
                        <label htmlFor={`${panel}-connection`} className={labelClass}>Connected {integration} account</label>
                        <OverrideConnection
                          id={`${panel}-connection`}
                          integration={integration}
                          declared={declared}
                          value={o.connectionId ?? ""}
                          shared={sharedConnections[integration] ?? ""}
                          onChange={(connectionId) => onPatch(name, { connectionId })}
                          onConnect={() => onConnect(integration)}
                        />
                      </div>
                    )}
                    <div>
                      <label htmlFor={`${panel}-prompt`} className={labelClass}>Prompt</label>
                      <textarea
                        id={`${panel}-prompt`}
                        className={textareaClass}
                        value={o.message ?? ""}
                        placeholder={promptOf(trigger) || sharedMessage || (needPrompt.includes(name) ? "This trigger has no prompt of its own." : "")}
                        onChange={(e) => onPatch(name, { message: e.target.value })}
                      />
                      <p className={hintClass}>Empty follows the prompt above, or the one the workflow declares.</p>
                    </div>
                  </div>
                )}
              </CardInset>
            </li>
          );
        })}
      </ul>
    </fieldset>
  );
}

function OverrideConnection({
  id,
  integration,
  declared,
  value,
  shared,
  onChange,
  onConnect,
}: {
  id: string;
  integration: string;
  declared: readonly DeclaredTrigger[];
  value: string;
  shared: string;
  onChange: (connectionId: string) => void;
  onConnect: () => void;
}) {
  const { entry } = useIntegrationEntry(integration, declared);
  if (!entry) return <p className={hintClass}>Uses your default {integration} connection.</p>;
  return (
    <div>
      <ConnectionPicker entry={entry} value={value || shared} onChange={onChange} onConnect={onConnect} />
      <span id={id} className="sr-only">Connected {integration} account</span>
    </div>
  );
}

/** Why nothing was turned on, trigger by trigger. */
function RefusalNotice({ verdict }: { verdict: Exclude<ReturnType<typeof summarizeEnable>, { kind: "enabled" }> }) {
  const partial = verdict.kind === "partial";
  const rows = [
    ...(partial ? verdict.running : []),
    ...verdict.failed,
    ...verdict.held,
  ];
  return (
    <div role="alert" className="rounded-md border border-danger-border bg-danger-surface px-3 py-3 text-sm text-danger-ink">
      <p className="flex items-start gap-2 font-medium">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
        {partial
          ? "Something is still running that you did not ask for."
          : refusalHeadline(verdict.failed)}
      </p>
      <ul aria-label="What happened to each trigger" className="mt-2 space-y-1.5">
        {rows.map((row) => (
          <li key={row.trigger} className="flex items-start gap-2 text-xs">
            <OutcomeIcon result={row} />
            <span>
              <span className="font-mono">{row.trigger}</span>
              {" — "}
              <span className="font-medium">{outcomeLabel(row.outcome)}.</span>{" "}
              {row.message}
            </span>
          </li>
        ))}
      </ul>
      {!partial && verdict.failed.length > 0 && (
        <p className="mt-2 text-xs">Fix {verdict.failed.length === 1 ? "that" : "those"} below and try again. Enabling is all or nothing, so none of the others were started either.</p>
      )}
    </div>
  );
}

function OutcomeIcon({ result }: { result: WorkflowTriggerResult }) {
  const cls = "mt-0.5 h-3.5 w-3.5 shrink-0";
  if (result.outcome === "failed") return <CircleX className={cls} aria-hidden />;
  if (result.outcome === "leftEnabled") return <AlertTriangle className={cls} aria-hidden />;
  if (result.outcome === "notEnabled") return <MinusCircle className={cls} aria-hidden />;
  return <CircleCheck className={cls} aria-hidden />;
}

function EnabledView({ workflowTitle, result, onClose }: { workflowTitle: string; result: EnableWorkflowResult; onClose: () => void }) {
  const hooks = result.results.filter((r) => r.activation && r.webhook);
  const resumed = result.results.filter((r) => r.outcome === "resumed").length;
  return (
    <Modal isOpen onClose={onClose} size="lg" title={`${workflowTitle} is on`}>
      <div className="space-y-4">
        <ul aria-label="Enabled triggers" className="space-y-1.5">
          {result.results.map((r) => (
            <li key={r.trigger} className="flex items-center gap-2 text-sm">
              <CircleCheck className="h-4 w-4 shrink-0 text-success-ink" aria-hidden />
              <span className="font-mono text-foreground">{r.trigger}</span>
              <span className="text-muted-foreground">{outcomeLabel(r.outcome)}</span>
            </li>
          ))}
        </ul>
        {resumed > 0 && (
          <p className={hintClass}>
            {resumed === 1 ? "A trigger you had paused was" : `${resumed} triggers you had paused were`} turned back on with the settings they already had.
          </p>
        )}
        {hooks.map((r) => (
          <div key={r.trigger} className="space-y-2">
            <p className="text-sm font-medium text-foreground">Webhook for <span className="font-mono">{r.trigger}</span></p>
            <WebhookCredentialPanel trigger={r.activation!} credential={r.webhook} />
          </div>
        ))}
        <div className="flex justify-end border-t border-border pt-4">
          <Button variant="primary" onClick={onClose}>Done</Button>
        </div>
      </div>
    </Modal>
  );
}
