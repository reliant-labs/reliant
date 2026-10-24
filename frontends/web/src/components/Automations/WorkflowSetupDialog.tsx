// Copyright (c) 2025 Reliant Labs

/**
 * Set up a workflow automation: one workflow turned on in one project
 * (research/autopilot/automation-ux-spec.md §3). The ONE dialog for it, in
 * three modes:
 *
 *   setup     "Set up PR review"            → Turn on
 *   finish    "Finish setting up PR review" → Save and turn on
 *             (NEEDS_SETUP: stored settings prefilled, the missing field
 *             focused, a banner saying what changed)
 *   settings  "PR review in reliant-labs"   → Save (Save and resume when paused)
 *
 * All three call EnableWorkflow, which creates the missing activations and
 * brings the existing ones to these settings (owner decision O2), so the
 * workflow's triggers are configured together and cannot drift apart.
 *
 * Sections, in the order a person decides (typed-inputs-design.md §7):
 *   (1) each integration: its connection, then who writes
 *   (2) What it watches: the inputs a trigger is scoped by, then the typed
 *       inputs scoped by those
 *   (3) Where it runs: project, machine, workspace
 *   (4) Who can ask it to work: people pickers
 *   (5) Settings: other typed and required inputs
 *   then More settings (inputs with defaults), the prompt when some trigger
 *   has none, the success notification, and per-trigger exceptions.
 * Which input lands where is lib/setupInputs.ts, decided from data.
 *
 * Validation is inline on blur and on submit; the primary button stays
 * enabled, and pressing it with gaps opens an error summary that takes focus
 * and links to each field. A server refusal (all or nothing) lands in the same
 * summary, mapped to a field where it names one, with every choice kept.
 */

import { useEffect, useId, useMemo, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ChevronDown, ChevronRight, CircleCheck, CircleX, MinusCircle } from "lucide-react";
import { toast } from "sonner";

import {
  triggerErrorMessage,
  triggerGrpc,
  type ActingIdentityKey,
  type EnableWorkflowInput,
  type EnableWorkflowResult,
  type Trigger,
  type WorkflowTriggerOverrideInput,
  type WorkflowTriggerResult,
} from "@/api/trigger-grpc";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { triggerKeys, useProjectDaemonInstalls, useTriggers } from "@/hooks/trigger-queries";
import { useCatalogEntry, useDeclaredTriggerRef, useDeclaredTriggerRefState, useIntegrationEventNaming } from "@/hooks/connection-queries";
import { useWorkflowLibrary } from "@/hooks/workflow-library-queries";
import { describeSchedule } from "@/lib/cronText";
import { describeDeclaredSource, integrationOf, isAlertingWebhook, promptOf, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { useListRepos } from "@/hooks/repo-service-hooks_gen";
import { DEFAULT_REPO_INPUT, singleGitHubRepo, withDefaultRepo } from "./oncallSetup";
import { inputDefToSchema } from "@/lib/nodeFieldAdapter";
import { getInputDescription, getInputMaxItems, getInputMinItems, getInputMulti, isIntegrationInput, type InputDef } from "@/lib/inputHelpers";
import { classifyInputs, type SetupInput } from "@/lib/setupInputs";
import { triggerRunsWorkflow } from "@/lib/triggerRail";
import { workflowPageHref } from "@/lib/workflowsArea";
import { formatAbsoluteTime } from "@/lib/relativeTime";
import { useProjectStore } from "@/store/projectStore";
import { useWorkflowPresets } from "@/hooks/preset-queries";
import { cn } from "@/lib/utils";
import { CardInset } from "../forge-ui/card";
import Button from "@/components/forge-ui/button";
import { ErrorSummary, focusField, type ErrorSummaryItem } from "./ErrorSummary";
import Modal from "../forge-ui/modal";
import ToggleSwitch from "../forge-ui/toggle_switch";
import { ProtoFieldRenderer } from "../workflow/ProtoFieldRenderer";
import { ConnectionPicker } from "../workflow/connections/ConnectionPicker";
import { ConnectIntegrationDialog } from "../workflow/connections/ConnectIntegrationDialog";
import { TypedInputField, TypedInputsProvider } from "../workflow/inputs/TypedInputField";
import { integrationDisplayName, inputTypeNouns } from "../workflow/inputs/inputTypeNouns";
import { RunNowDialog } from "../workflow/run/RunNowDialog";
import { RunWorkflowForm, WorktreeField } from "../workflow/run/RunWorkflowForm";
import { errorTextClass, fieldClass, hintClass, labelClass, textareaClass } from "../workflow/run/runFormStyles";
import {
  coerceInputValue,
  flattenParams,
  groupNamesOf,
  nestParams,
  presetFlatValues,
  toDisplayValue,
  type RunWorkflowValue,
} from "../workflow/run/runWorkflowValues";
import { useWorkflowDefinition } from "../workflow/useWorkflowDefinition";
import { ActingIdentityField } from "./ActingIdentityField";
import { buildDaemonChoices, defaultDaemonId, NO_MACHINE } from "./daemonChoices";
import { outcomeLabel, planEnable, refusalHeadline, summarizeEnable, triggersNeedingPrompt, type EnableVerdict } from "./enableWorkflow";
import { SetupSection } from "./setup/SetupSection";
import { joinOr, SetupSuccess } from "./setup/SetupSuccess";

export type SetupMode = "setup" | "finish" | "settings";

export interface WorkflowSetupDialogProps {
  open: boolean;
  onClose: () => void;
  /** The stored workflow ref the triggers are declared in. */
  workflowRef: string;
  workflowTitle?: string;
  /** Every trigger the workflow declares, in declaration order. */
  declared: readonly DeclaredTrigger[];
  /** The project it runs in. Fixed in finish and settings mode. */
  defaultProjectId?: string;
  /** Forces a mode; otherwise it follows what exists in the project. */
  mode?: SetupMode;
  /** A field to focus once the form is ready: an input name, "machine" or "connection". */
  focus?: string;
  /** Offer "Open workflow" on success (the dialog was opened outside the workflow page). */
  showOpenWorkflow?: boolean;
  /** Called once every trigger is running. */
  onEnabled?: (result: EnableWorkflowResult) => void;
}

export function WorkflowSetupDialog(props: WorkflowSetupDialogProps) {
  if (!props.open) return null;
  return <SetupBody {...props} />;
}

interface Override {
  machine?: string;
  connectionId?: string;
  message?: string;
}

interface FieldError {
  key: string;
  fieldId: string;
  message: string;
}

/** The mode the existing activations call for, when the host did not choose. */
export function inferSetupMode(existing: readonly Trigger[]): SetupMode {
  if (existing.length === 0) return "setup";
  return existing.some((t) => t.health.status === "needs_setup") ? "finish" : "settings";
}

/** The shared settings and per-trigger exceptions stored on existing activations. */
export function prefillFromActivations(existing: readonly Trigger[], declared: readonly DeclaredTrigger[]) {
  const sorted = [...existing].sort((a, b) => (a.workflowTrigger ?? "").localeCompare(b.workflowTrigger ?? ""));
  const first = sorted[0];
  const machineOf = (t: Trigger) => (t.noMachine ? NO_MACHINE : t.daemonId);
  const params: Record<string, unknown> = {};
  // A trigger that maps an input from its event stores no param of that name,
  // so the shared params are the union, first activation first.
  for (const t of sorted) for (const [k, v] of Object.entries(t.params)) if (!(k in params)) params[k] = v;
  const sharedMessage = sorted.find((t) => !declared.find((d) => d.name === t.workflowTrigger && promptOf(d)))?.message ?? "";
  const connections: Record<string, string> = {};
  for (const t of sorted) {
    const integration = declared.find((d) => d.name === t.workflowTrigger);
    const id = integration && integrationOf(integration)?.integration;
    if (id && t.connectionId && !connections[id]) connections[id] = t.connectionId;
  }
  const overrides: Record<string, Override> = {};
  for (const t of sorted) {
    const name = t.workflowTrigger ?? "";
    const own = declared.find((d) => d.name === name);
    const o: Override = {};
    if (machineOf(t) !== machineOf(first)) o.machine = machineOf(t);
    const integration = own && integrationOf(own)?.integration;
    if (integration && t.connectionId && t.connectionId !== connections[integration]) o.connectionId = t.connectionId;
    const ownPrompt = own ? !!promptOf(own) : false;
    if (ownPrompt ? !!t.message : t.message !== sharedMessage) o.message = t.message;
    if (o.machine || o.connectionId || o.message) overrides[name] = o;
  }
  return {
    machine: machineOf(first),
    connections,
    actingIdentity: first.actingIdentity,
    value: { presets: { ...first.presets }, params, worktreeId: first.worktreeId } satisfies RunWorkflowValue,
    message: sharedMessage,
    notify: first.notifyOnComplete,
    overrides,
    paused: sorted.every((t) => !t.enabled),
    since: first.updatedAt,
    needsSetupDetail: sorted.find((t) => t.health.status === "needs_setup")?.health.lastFailureDetail,
  };
}

function isEmpty(value: unknown): boolean {
  return value === undefined || value === null || value === "" || (Array.isArray(value) && value.length === 0);
}

function requiredMessage(input: SetupInput, label: string): string {
  if (isIntegrationInput(input.schema)) {
    const nouns = inputTypeNouns(input.schema.type ?? "");
    if (getInputMulti(input.schema)) {
      const min = getInputMinItems(input.schema) ?? 1;
      return min > 1 ? `Choose at least ${min} ${nouns.plural}.` : `Choose at least one ${nouns.singular}.`;
    }
    return `Choose a ${nouns.singular}.`;
  }
  return `Fill in ${label}.`;
}

function SetupBody({
  onClose,
  workflowRef,
  workflowTitle,
  declared,
  defaultProjectId,
  mode: forcedMode,
  focus,
  showOpenWorkflow = false,
  onEnabled,
}: WorkflowSetupDialogProps) {
  const ids = useId();
  const fid = (key: string) => `${ids}-${key.replace(/[^a-zA-Z0-9_-]/g, "_")}`;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const projects = useProjectStore((s) => s.projects);
  const currentProjectId = useProjectStore((s) => s.currentProject?.id);
  const naming = useIntegrationEventNaming();
  const title = workflowTitle || workflowRef;

  const plan = useMemo(() => planEnable(declared), [declared]);
  const needPrompt = useMemo(() => triggersNeedingPrompt(declared), [declared]);

  const [projectId, setProjectId] = useState(defaultProjectId ?? currentProjectId ?? projects[0]?.id ?? "");
  const triggersQuery = useTriggers(projectId || undefined, { enabled: !!projectId });
  const existing = useMemo(
    () => (triggersQuery.data ?? []).filter((t) => t.workflowTrigger && t.projectId === projectId && triggerRunsWorkflow(t, workflowRef)),
    [triggersQuery.data, projectId, workflowRef],
  );
  const mode: SetupMode = forcedMode ?? inferSetupMode(existing);
  const projectName = projects.find((p) => p.id === projectId)?.name ?? existing[0]?.projectName ?? "this project";

  const library = useWorkflowLibrary(projectId || undefined);
  const listing = library.data?.workflows.find((w) => triggerRunsWorkflow({ workflow: w.name }, workflowRef));
  const needsNoMachine = listing?.needsMachine !== undefined && listing.needsMachine.length === 0;
  const definition = useWorkflowDefinition(projectId || undefined, workflowRef);
  const inputs = useMemo(() => classifyInputs(definition.inputGroups, declared), [definition.inputGroups, declared]);
  const groupNames = useMemo(() => groupNamesOf(definition.inputGroups), [definition.inputGroups]);
  const { presets: availablePresets } = useWorkflowPresets(projectId || undefined, workflowRef);

  const [machine, setMachine] = useState("");
  const [machineChosen, setMachineChosen] = useState(false);
  const [connections, setConnections] = useState<Record<string, string>>({});
  const [actingIdentity, setActingIdentity] = useState<ActingIdentityKey>("owner");
  const [value, setValue] = useState<RunWorkflowValue>({ presets: {}, params: {} });
  const valueRef = useRef(value);
  valueRef.current = value;
  const [message, setMessage] = useState("");
  const [notify, setNotify] = useState(false);
  const [overrides, setOverrides] = useState<Record<string, Override>>({});
  const [advancedOpen, setAdvancedOpen] = useState<Record<string, boolean>>({});
  const [moreOpen, setMoreOpen] = useState(false);
  const [promptOpen, setPromptOpen] = useState(needPrompt.length > 0);
  const [eventsOpen, setEventsOpen] = useState(false);
  const [connectTarget, setConnectTarget] = useState<string | null>(null);
  const [touched, setTouched] = useState<Set<string>>(new Set());
  const [attempts, setAttempts] = useState(0);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<EnableWorkflowResult | null>(null);
  const [trying, setTrying] = useState(false);
  const [prefill, setPrefill] = useState<ReturnType<typeof prefillFromActivations> | null>(null);

  // Existing activations fill the form once per project: finish and settings
  // start from what was chosen, and setting up in a project where some
  // triggers are already on must not wipe them (Enable updates them too).
  const prefilledFor = useRef<string | null>(null);
  useEffect(() => {
    if (!triggersQuery.isSuccess || prefilledFor.current === projectId) return;
    prefilledFor.current = projectId;
    if (existing.length === 0) return;
    const p = prefillFromActivations(existing, declared);
    setPrefill(p);
    setMachine(p.machine);
    setMachineChosen(true);
    setConnections(p.connections);
    setActingIdentity(p.actingIdentity);
    setValue(p.value);
    setMessage(p.message);
    setNotify(p.notify);
    setOverrides(p.overrides);
    if (p.message) setPromptOpen(true);
  }, [triggersQuery.isSuccess, existing, declared, projectId]);

  const { daemons, loading: daemonsLoading } = useDaemonStatus();
  const installs = useProjectDaemonInstalls();
  const choices = useMemo(() => buildDaemonChoices(daemons, installs.data ?? [], projectId), [daemons, installs.data, projectId]);
  useEffect(() => {
    if (machineChosen || daemonsLoading || installs.isLoading || !library.isSuccess) return;
    // A workflow that needs no machine runs without one unless the user says otherwise.
    setMachine(needsNoMachine ? NO_MACHINE : (defaultDaemonId(choices) ?? ""));
  }, [choices, machineChosen, daemonsLoading, installs.isLoading, library.isSuccess, needsNoMachine]);

  // What every input shows: presets first, explicit values over them.
  const flat = useMemo(
    () => ({ ...presetFlatValues(value.presets, availablePresets), ...flattenParams(value.params, groupNames) }),
    [value.presets, value.params, availablePresets, groupNames],
  );
  const schemaByName = useMemo(() => {
    const map = new Map<string, InputDef>();
    for (const g of definition.inputGroups) for (const i of g.inputs) map.set(i.name, i.schema);
    return map;
  }, [definition.inputGroups]);

  // An on-call workflow (a `source: alerting` webhook) files alerts no
  // service maps to in default_repo: with one GitHub repository in the
  // project, that is the repository, so the one click needs no other input.
  const alerting = useMemo(() => declared.some(isAlertingWebhook), [declared]);
  const wantsDefaultRepo = alerting && !!projectId && schemaByName.has(DEFAULT_REPO_INPUT);
  const projectRepos = useListRepos({ projectId }, { enabled: wantsDefaultRepo, staleTime: 60_000 });
  const defaultRepo = wantsDefaultRepo ? singleGitHubRepo(projectRepos.data?.repos.map((r) => r.remoteUrl) ?? []) : undefined;
  const fillsDefaultRepo = withDefaultRepo(value.params, defaultRepo) !== value.params;

  const display = useMemo(() => {
    const out: Record<string, unknown> = {};
    for (const [name, raw] of Object.entries(flat)) {
      const schema = schemaByName.get(name);
      out[name] = schema ? toDisplayValue(schema, raw) : raw;
    }
    return out;
  }, [flat, schemaByName]);
  const labelOf = (name: string) => {
    const schema = schemaByName.get(name);
    return schema ? inputDefToSchema(name, schema).label : name;
  };

  const setInput = (name: string, raw: unknown) => {
    const current = valueRef.current;
    const schema = schemaByName.get(name);
    const next = schema ? coerceInputValue(schema, raw) : raw;
    const f = flattenParams(current.params, groupNames);
    if (next === undefined) delete f[name];
    else f[name] = next;
    const updated = { ...current, params: nestParams(f, groupNames) };
    valueRef.current = updated;
    setValue(updated);
  };

  // ---- validation ----------------------------------------------------------
  const asked = [...inputs.watches, ...inputs.people, ...inputs.settings];
  const fieldErrors: FieldError[] = [];
  if (!projectId) fieldErrors.push({ key: "project", fieldId: fid("project"), message: "Choose a project." });
  for (const input of asked) {
    const current = flat[input.name];
    if (input.required && isEmpty(current)) {
      fieldErrors.push({ key: input.name, fieldId: fid(`in-${input.name}`), message: requiredMessage(input, labelOf(input.name)) });
      continue;
    }
    const max = getInputMaxItems(input.schema);
    const min = getInputMinItems(input.schema);
    if (Array.isArray(current) && max !== undefined && current.length > max) {
      fieldErrors.push({ key: input.name, fieldId: fid(`in-${input.name}`), message: `Choose at most ${max}.` });
    } else if (Array.isArray(current) && min !== undefined && current.length > 0 && current.length < min) {
      fieldErrors.push({ key: input.name, fieldId: fid(`in-${input.name}`), message: `Choose at least ${min}.` });
    }
  }
  if (!machine) {
    fieldErrors.push({
      key: "machine",
      fieldId: fid("machine"),
      message: daemons.length === 0 ? "Connect a machine first, or choose No machine." : "Choose where the runs execute.",
    });
  }
  const unwritten = message.trim() ? [] : needPrompt.filter((name) => !overrides[name]?.message?.trim());
  if (unwritten.length > 0) {
    fieldErrors.push({
      key: "message",
      fieldId: fid("message"),
      message: `Write the prompt each run starts from: ${unwritten.map((n) => `“${n}”`).join(", ")} ${unwritten.length === 1 ? "has" : "have"} none of its own.`,
    });
  }
  const errorFor = (key: string) => {
    const e = fieldErrors.find((f) => f.key === key);
    return e && (attempts > 0 || touched.has(key)) ? e.message : undefined;
  };
  const touch = (key: string) => setTouched((prev) => (prev.has(key) ? prev : new Set(prev).add(key)));
  const blurOut = (key: string) => (event: React.FocusEvent<HTMLElement>) => {
    if (!event.currentTarget.contains(event.relatedTarget as Node | null)) touch(key);
  };

  // Focus the field the host named (Finish setup → the missing input) once it exists.
  const focused = useRef(false);
  useEffect(() => {
    if (focused.current || definition.loading || !triggersQuery.isSuccess) return;
    const target = focus ?? (mode === "finish" ? fieldErrors[0]?.key : undefined);
    if (!target) return;
    const id = target === "machine" || target === "project" || target === "message" ? fid(target) : fid(`in-${target}`);
    const timer = setTimeout(() => {
      if (document.getElementById(id)) {
        focused.current = true;
        focusField(id);
      }
    }, 0);
    return () => clearTimeout(timer);
  });

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
      worktreeId: machine === NO_MACHINE ? undefined : value.worktreeId,
      daemonId: machine === NO_MACHINE ? "" : machine,
      noMachine: machine === NO_MACHINE,
      connectionIds: Object.fromEntries(Object.entries(connections).filter(([, id]) => id)),
      params: withDefaultRepo(value.params, defaultRepo),
      presets: value.presets,
      notifyOnComplete: notify,
      message: message.trim(),
      namePrefix: workflowTitle || undefined,
      overrides: overrideInputs,
      actingIdentity,
    };
  };

  const onSubmit = async (event?: FormEvent) => {
    event?.preventDefault();
    setAttempts((n) => n + 1);
    setError(null);
    setResult((prev) => (prev && !prev.enabled ? prev : null));
    if (fieldErrors.length > 0) return;
    setSaving(true);
    try {
      const outcome = await triggerGrpc.enableWorkflow(buildInput());
      void queryClient.invalidateQueries({ queryKey: triggerKeys.lists() });
      setResult(outcome);
      if (outcome.enabled) {
        toast.success(mode === "settings" ? `${title} in ${projectName} is saved` : `${title} is on in ${projectName}`);
        onEnabled?.(outcome);
      }
    } catch (err) {
      setError(triggerErrorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  const verdict = result ? summarizeEnable(result) : null;

  // ---- success -------------------------------------------------------------
  if (trying) {
    return <RunNowDialog open onClose={onClose} projectId={projectId} workflowRef={workflowRef} />;
  }
  if (verdict?.kind === "enabled") {
    const scopeValues = inputs.watches
      .filter((i) => i.scopes.length > 0)
      .flatMap((i) => (Array.isArray(flat[i.name]) ? (flat[i.name] as unknown[]).map(String) : flat[i.name] ? [String(flat[i.name])] : []));
    return (
      <Modal open onClose={onClose} size="lg" title={mode === "settings" ? `${title} in ${projectName}` : `${title} is on`}>
        <SetupSuccess
          workflowTitle={title}
          projectName={projectName}
          saved={mode === "settings"}
          result={result!}
          declared={declared}
          scopeSummary={scopeValues.length > 0 ? joinOr(scopeValues) : undefined}
          onDone={onClose}
          onTryNow={plan.integrations.length > 0 ? () => setTrying(true) : undefined}
          onOpenWorkflow={
            showOpenWorkflow ? () => void navigate({ href: workflowPageHref(workflowRef, projectId, { automation: true }) }) : undefined
          }
        />
      </Modal>
    );
  }

  // ---- form ----------------------------------------------------------------
  const summaryItems: ErrorSummaryItem[] = [
    ...(attempts > 0 ? fieldErrors.map((e) => ({ fieldId: e.fieldId, message: e.message })) : []),
    ...(verdict ? refusalItems(verdict, (key) => fid(key), asked, plan.integrations) : []),
    ...(error ? [{ message: error }] : []),
  ];
  const showSummary = summaryItems.length > 0 && (attempts > 0 || !!verdict || !!error);
  const toFix = summaryItems.length;
  const machineLabel =
    machine === NO_MACHINE ? "no machine" : (choices.find((c) => c.daemonId === machine)?.label ?? (machine ? "a machine" : "no machine chosen"));
  const footerText = showSummary
    ? `${toFix} ${toFix === 1 ? "thing" : "things"} to fix`
    : fieldErrors.length > 0
      ? `${fieldErrors.length} required ${fieldErrors.length === 1 ? "field" : "fields"} left`
      : `Runs in ${projectName} · ${machineLabel}`;
  const paused = mode === "settings" && !!prefill?.paused;
  const primaryLabel = mode === "setup" ? "Turn on" : mode === "finish" ? "Save and turn on" : paused ? "Save and resume" : "Save";
  const dialogTitle = mode === "setup" ? `Set up ${title}` : mode === "finish" ? `Finish setting up ${title}` : `${title} in ${projectName}`;
  const integrationEvents = declared.flatMap((t) => integrationOf(t)?.events ?? []);
  const integrationName = (id: string) => {
    const named = naming.integration(id);
    return named && named !== id ? named : integrationDisplayName(id);
  };
  const moreChanged = inputs.more.filter((i) => flat[i.name] !== undefined && value.params && i.name in flattenParams(value.params, groupNames)).length;
  const fieldIds = Object.fromEntries(asked.map((i) => [i.name, fid(`in-${i.name}`)]));
  const typedErrors = Object.fromEntries(asked.map((i) => [i.name, errorFor(i.name)]).filter(([, e]) => e)) as Record<string, string>;
  let section = 0;

  const footer = (
    <div className="flex flex-col-reverse gap-2 sm:flex-row sm:items-center sm:justify-between">
      <p className={cn("text-xs", showSummary ? "text-danger-ink" : "text-muted-foreground")} data-testid="setup-footer-summary">
        {footerText}
      </p>
      <div className="flex gap-2 max-sm:w-full">
        <Button type="button" variant="outline" onClick={onClose} className="max-sm:hidden">
          {verdict?.kind === "partial" ? "Close" : "Cancel"}
        </Button>
        <Button type="submit" form={fid("form")} variant="primary" loading={saving} className="max-sm:w-full">
          {primaryLabel}
        </Button>
      </div>
    </div>
  );

  const renderInput = (input: SetupInput, hint?: string) => {
    const err = errorFor(input.name);
    if (isIntegrationInput(input.schema)) {
      return (
        <div key={input.name} onBlur={blurOut(input.name)}>
          <TypedInputField name={input.name} schema={input.schema} value={display[input.name]} values={flat} onChange={(v) => setInput(input.name, v)} />
          {hint && <p className={hintClass}>{hint}</p>}
        </div>
      );
    }
    return (
      <div key={input.name} id={fid(`in-${input.name}`)} tabIndex={-1} onBlur={blurOut(input.name)} className="cpv2-scope focus:outline-none">
        <ProtoFieldRenderer
          schema={inputDefToSchema(input.name, input.schema)}
          value={display[input.name]}
          onChange={(v) => setInput(input.name, v)}
          hideCELToggle
        />
        {err && <p className={errorTextClass}>{err}</p>}
      </div>
    );
  };

  return (
    <Modal open onClose={onClose} size="lg" title={dialogTitle} footer={footer}>
      <TypedInputsProvider
        value={{
          connectionIds: connections,
          actingIdentity,
          onConnect: (integration) => setConnectTarget(integration),
          integrationNames: Object.fromEntries(plan.integrations.map((i) => [i, integrationName(i)])),
          inputLabels: Object.fromEntries([...schemaByName.keys()].map((n) => [n, labelOf(n)])),
          inputTypes: Object.fromEntries([...schemaByName.entries()].map(([n, s]) => [n, s.type ?? ""])),
          errors: typedErrors,
          fieldIds,
        }}
      >
        <form id={fid("form")} onSubmit={onSubmit} noValidate className="space-y-6" aria-label={dialogTitle}>
          <div className="space-y-1.5 text-sm">
            {listing?.description && <p className="line-clamp-3 text-muted-foreground">{listing.description}</p>}
            <button
              type="button"
              aria-expanded={eventsOpen}
              onClick={() => setEventsOpen((o) => !o)}
              className="inline-flex items-center gap-1 rounded-sm text-xs font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            >
              {eventsOpen ? <ChevronDown className="h-3.5 w-3.5" aria-hidden /> : <ChevronRight className="h-3.5 w-3.5" aria-hidden />}
              {plan.integrations.length > 0
                ? `Listens for ${integrationEvents.length} ${integrationName(plan.integrations[0])} ${integrationEvents.length === 1 ? "event" : "events"}`
                : `${declared.length} ${declared.length === 1 ? "trigger" : "triggers"}`}
            </button>
            {eventsOpen && (
              <ul aria-label="What starts it" className="space-y-1 pl-5 text-xs text-muted-foreground">
                {declared.map((t) => (
                  <li key={t.name}>
                    <span className="font-mono text-foreground">{t.name}</span> · {describeDeclaredSource(t, describeSchedule, naming)}
                  </li>
                ))}
              </ul>
            )}
          </div>

          {alerting && (
            <div role="note" className="space-y-1 rounded-md border border-border bg-muted/40 px-3 py-2 text-sm" data-testid="oncall-note">
              <p className="text-foreground">
                On Reliant&apos;s hosted platform, turning this on connects it to your organization&apos;s alerts: the critical and error
                alerts of {projectName}&apos;s environments, and new, regressed or spiking errors. There is no token to copy and no alert route to write.
              </p>
              {fillsDefaultRepo && defaultRepo && (
                <p className="text-xs text-muted-foreground">
                  Alerts no service is mapped to are filed in <span className="font-mono">{defaultRepo}</span>, this project&apos;s repository.
                  Set default_repo under More settings to choose another.
                </p>
              )}
            </div>
          )}

          {mode === "finish" && prefill && (
            <div role="note" className="rounded-md border border-warning-border bg-warning-surface px-3 py-2 text-sm text-warning-ink" data-testid="finish-banner">
              <span className="font-semibold">Not running yet.</span>{" "}
              {prefill.needsSetupDetail || "A setting this workflow now requires is missing."} Everything else keeps the settings you chose on{" "}
              {formatAbsoluteTime(prefill.since)}.
            </div>
          )}

          {showSummary && (
            <ErrorSummary
              title={verdict?.kind === "partial" ? "Something is still running that you did not ask for." : verdict ? refusalHeadline(verdict.kind === "refused" ? verdict.failed : []) : "Fix these to continue"}
              items={summaryItems}
              focusKey={`${attempts}:${result ? "r" : ""}`}
            >
              {verdict && <OutcomeList verdict={verdict} />}
            </ErrorSummary>
          )}

          {plan.integrations.map((integration, index) => (
            <SetupSection key={integration} number={++section} title={integrationName(integration)}>
              <div id={fid(`connection-${integration}`)} tabIndex={-1} className="focus:outline-none">
                <IntegrationConnection
                  integration={integration}
                  declared={declared}
                  value={connections[integration] ?? ""}
                  onChange={(id) => setConnections((prev) => ({ ...prev, [integration]: id }))}
                  onConnect={() => setConnectTarget(integration)}
                />
              </div>
              {index === 0 && <ActingIdentityField value={actingIdentity} onChange={setActingIdentity} />}
            </SetupSection>
          ))}

          {inputs.watches.length > 0 && (
            <SetupSection number={++section} title="What it watches" testId="section-watches">
              {inputs.watches.map((input) =>
                renderInput(
                  input,
                  // The author's description says what the scope is for; without one, say it here.
                  input.scopes.length > 0 && !getInputDescription(input.schema)
                    ? `Only events here start ${input.scopes.length === 1 ? `“${input.scopes[0]}”` : "its triggers"}. Nothing else is touched.`
                    : undefined,
                ),
              )}
            </SetupSection>
          )}

          <SetupSection number={++section} title="Where it runs" testId="section-where">
            <div className="grid gap-4 sm:grid-cols-2">
              <div onBlur={blurOut("project")}>
                <label htmlFor={fid("project")} className={labelClass}>
                  Project
                </label>
                {mode === "setup" ? (
                  <select
                    id={fid("project")}
                    className={fieldClass}
                    value={projectId}
                    aria-invalid={!!errorFor("project")}
                    onChange={(e) => {
                      setProjectId(e.target.value);
                      setValue((prev) => ({ ...prev, worktreeId: undefined }));
                    }}
                  >
                    {projects.length === 0 && <option value="">No projects</option>}
                    {projects.map((project) => (
                      <option key={project.id} value={project.id}>
                        {project.name}
                      </option>
                    ))}
                  </select>
                ) : (
                  <p id={fid("project")} className="flex h-9 items-center text-sm text-foreground">
                    {projectName}
                  </p>
                )}
                {errorFor("project") && <p className={errorTextClass}>{errorFor("project")}</p>}
              </div>
              <div onBlur={blurOut("machine")}>
                <label htmlFor={fid("machine")} className={labelClass}>
                  Runs on
                </label>
                <MachineSelect
                  id={fid("machine")}
                  value={machine}
                  loading={daemonsLoading}
                  choices={choices}
                  onChange={(v) => {
                    setMachine(v);
                    setMachineChosen(true);
                  }}
                  invalid={!!errorFor("machine")}
                />
                <p className={hintClass}>
                  {needsNoMachine
                    ? "This workflow needs none: it works through its integrations."
                    : listing?.needsMachine?.length
                      ? "It edits code, so it needs a machine with your checkout."
                      : machine === NO_MACHINE
                        ? "Runs use only web, integration and planning tools; nothing touches files."
                        : "Every tool call in each run executes on this machine."}
                </p>
                {errorFor("machine") && <p className={errorTextClass}>{errorFor("machine")}</p>}
              </div>
            </div>
            {machine && machine !== NO_MACHINE && projectId && (
              <WorktreeField
                id={fid("worktree")}
                projectId={projectId}
                worktreeId={value.worktreeId}
                disabled={false}
                onChange={(worktreeId) => setValue((prev) => ({ ...prev, worktreeId }))}
              />
            )}
          </SetupSection>

          {inputs.people.length > 0 && (
            <SetupSection number={++section} title="Who can ask it to work" testId="section-people">
              {inputs.people.map((input) => renderInput(input))}
            </SetupSection>
          )}

          {inputs.settings.length > 0 && (
            <SetupSection number={++section} title="Settings" testId="section-settings">
              {inputs.settings.map((input) => renderInput(input))}
            </SetupSection>
          )}

          {definition.loading && <p className={hintClass} aria-live="polite">Loading this workflow&apos;s inputs…</p>}
          {definition.error && (
            <p className={errorTextClass} role="alert">
              Could not load this workflow&apos;s inputs: {definition.error}
            </p>
          )}

          {inputs.more.length > 0 && projectId && (
            <Disclosure
              open={moreOpen}
              onToggle={() => setMoreOpen((o) => !o)}
              title="More settings"
              summary={`${inputs.more
                .slice(0, 3)
                .map((i) => labelOf(i.name))
                .join(", ")}${inputs.more.length > 3 ? `, +${inputs.more.length - 3}` : ""} · ${moreChanged > 0 ? `${moreChanged} changed` : "all defaults"}`}
            >
              <RunWorkflowForm
                projectId={projectId}
                workflowRef={workflowRef}
                value={value}
                onChange={(next) => {
                  valueRef.current = next;
                  setValue(next);
                }}
                applyDefaultPresets={mode === "setup"}
                excludeInputs={[...inputs.mapped, ...asked.map((i) => i.name)]}
                hideWorktree
                hideInputsLabel
              />
            </Disclosure>
          )}

          <Disclosure
            open={promptOpen}
            onToggle={() => setPromptOpen((o) => !o)}
            title={needPrompt.length > 0 ? "Prompt (required)" : "Prompt"}
            summary={needPrompt.length === 0 ? "Every trigger brings its own" : message.trim() ? "Written" : "Not written yet"}
          >
            <div onBlur={blurOut("message")}>
              <label htmlFor={fid("message")} className="sr-only">
                Prompt
              </label>
              <textarea
                id={fid("message")}
                className={textareaClass}
                value={message}
                onChange={(e) => setMessage(e.target.value)}
                placeholder={
                  needPrompt.length === 0
                    ? "Optional: every trigger already has a prompt in the workflow."
                    : "Each run starts from this message. Nobody will be watching, so say everything the agent needs."
                }
                aria-invalid={!!errorFor("message")}
              />
              <p className={hintClass}>
                {needPrompt.length === 0
                  ? "Every trigger brings its own prompt, and these words are not used."
                  : needPrompt.length === declared.length
                    ? "Used by every trigger, since none of them declares a prompt."
                    : `Used by ${needPrompt.map((n) => `“${n}”`).join(", ")}, which declare no prompt. The others keep theirs.`}
              </p>
              {errorFor("message") && <p className={errorTextClass}>{errorFor("message")}</p>}
            </div>
          </Disclosure>

          <div>
            <ToggleSwitch
              checked={notify}
              onChange={setNotify}
              label="Also notify me when a run succeeds"
              size="lg"
              className="forge-ui"
            />
            <p className={hintClass}>Failures always notify you.</p>
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
        </form>
      </TypedInputsProvider>

      <ConnectFor
        integration={connectTarget}
        declared={declared}
        onClose={() => setConnectTarget(null)}
        onConnected={(integration, id) => setConnections((prev) => ({ ...prev, [integration]: id }))}
      />
    </Modal>
  );
}

/**
 * A server refusal as error-summary links: a trigger's failure goes to the
 * field it names — a connection to its integration section, a param to its
 * input — and otherwise stands on its own.
 */
export function refusalItems(
  verdict: Exclude<EnableVerdict, { kind: "enabled" }>,
  fieldId: (key: string) => string,
  asked: readonly SetupInput[],
  integrations: readonly string[],
): ErrorSummaryItem[] {
  // What is still running is listed with the other triggers' outcomes
  // (OutcomeList); the links are for what failed and can be fixed here.
  return verdict.failed.map((r) => {
    const text = r.message || outcomeLabel(r.outcome);
    const param = asked.find((i) => new RegExp(`params\\.${i.name}\\b|\\b${i.name}\\b is`).test(text));
    if (param) return { fieldId: fieldId(`in-${param.name}`), message: `${r.trigger}: ${text}` };
    const integration = integrations.find((i) => new RegExp(`connect(ion)?\\b[^.]*\\b${i}\\b|\\b${i}\\b[^.]*connection`, "i").test(text));
    if (integration) return { fieldId: fieldId(`connection-${integration}`), message: `${r.trigger}: ${text}` };
    if (/machine|daemon/i.test(text)) return { fieldId: fieldId("machine"), message: `${r.trigger}: ${text}` };
    return { message: `${r.trigger}: ${text}` };
  });
}

function OutcomeList({ verdict }: { verdict: Exclude<EnableVerdict, { kind: "enabled" }> }) {
  const rows = [...(verdict.kind === "partial" ? verdict.running : []), ...verdict.held];
  if (rows.length === 0) return null;
  return (
    <ul aria-label="What happened to the other triggers" className="mt-2 space-y-1 text-xs">
      {rows.map((row) => (
        <li key={row.trigger} className="flex items-start gap-2">
          <OutcomeIcon result={row} />
          <span>
            <span className="font-mono">{row.trigger}</span> — <span className="font-medium">{outcomeLabel(row.outcome)}.</span> {row.message}
          </span>
        </li>
      ))}
    </ul>
  );
}

function OutcomeIcon({ result }: { result: WorkflowTriggerResult }) {
  const cls = "mt-0.5 h-3.5 w-3.5 shrink-0";
  if (result.outcome === "failed") return <CircleX className={cls} aria-hidden />;
  if (result.outcome === "leftEnabled") return <AlertTriangle className={cls} aria-hidden />;
  if (result.outcome === "notEnabled") return <MinusCircle className={cls} aria-hidden />;
  return <CircleCheck className={cls} aria-hidden />;
}

function Disclosure({
  open,
  onToggle,
  title,
  summary,
  children,
}: {
  open: boolean;
  onToggle: () => void;
  title: string;
  summary?: string;
  children: React.ReactNode;
}) {
  const panelId = useId();
  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={onToggle}
        className="flex w-full items-baseline gap-2 rounded-sm text-left text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        {open ? <ChevronDown className="h-4 w-4 shrink-0 self-center" aria-hidden /> : <ChevronRight className="h-4 w-4 shrink-0 self-center" aria-hidden />}
        <span className="font-semibold text-foreground">{title}</span>
        {summary && <span className="min-w-0 truncate text-xs text-muted-foreground">{summary}</span>}
      </button>
      {/* Kept mounted while closed, so a field an error links to still exists. */}
      <div id={panelId} hidden={!open} className="mt-3">
        {children}
      </div>
    </div>
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
          {choice.label} ({choice.statusLabel}
          {choice.ineligibleReason ? `, ${choice.ineligibleReason}` : ""})
        </option>
      ))}
      <option value={NO_MACHINE}>No machine (server tools only)</option>
    </select>
  );
}

/** The connected account an integration's triggers listen through. */
function IntegrationConnection({
  integration,
  declared,
  value,
  onChange,
  onConnect,
}: {
  integration: string;
  declared: readonly DeclaredTrigger[];
  value: string;
  onChange: (connectionId: string) => void;
  onConnect: () => void;
}) {
  const { entry, loading } = useIntegrationEntry(integration, declared);
  if (!entry) {
    return (
      <p className={hintClass} aria-live="polite">
        {loading
          ? `Loading ${integration}…`
          : `Couldn't look up how ${integration} connects. Your default ${integration} connection is used; if you have none, turning it on will say so.`}
      </p>
    );
  }
  return <ConnectionPicker entry={entry} value={value} onChange={onChange} onConnect={onConnect} compact />;
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
      target={
        integration && ref
          ? { ref, integrationId: integration, displayName: entry?.summary.integration.displayName ?? integration, icon: entry?.summary.integration.icon }
          : null
      }
      onClose={onClose}
      onConnected={(connection) => integration && onConnected(integration, connection.id)}
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
  const [shown, setShown] = useState(Object.keys(overrides).length > 0);
  const customised = declared.filter((t) => {
    const o = overrides[t.name ?? ""] ?? {};
    return !!(o.machine || o.connectionId || o.message?.trim());
  }).length;
  return (
    <fieldset className="space-y-2">
      <legend className="sr-only">Per-trigger exceptions</legend>
      <button
        type="button"
        aria-expanded={shown}
        onClick={() => setShown((s) => !s)}
        className="flex w-full items-baseline gap-2 rounded-sm text-left text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        {shown ? <ChevronDown className="h-4 w-4 shrink-0 self-center" aria-hidden /> : <ChevronRight className="h-4 w-4 shrink-0 self-center" aria-hidden />}
        <span className="font-semibold text-foreground">Per-trigger exceptions</span>
        <span className="text-xs text-muted-foreground">{customised > 0 ? `${customised} customised` : "none"}</span>
      </button>
      {shown && (
        <>
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
                          <label htmlFor={`${panel}-machine`} className={labelClass}>
                            Runs on
                          </label>
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
                          <OverrideConnection
                            integration={integration}
                            declared={declared}
                            value={o.connectionId ?? ""}
                            shared={sharedConnections[integration] ?? ""}
                            onChange={(connectionId) => onPatch(name, { connectionId })}
                            onConnect={() => onConnect(integration)}
                          />
                        )}
                        <div>
                          <label htmlFor={`${panel}-prompt`} className={labelClass}>
                            Prompt
                          </label>
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
        </>
      )}
    </fieldset>
  );
}

function OverrideConnection({
  integration,
  declared,
  value,
  shared,
  onChange,
  onConnect,
}: {
  integration: string;
  declared: readonly DeclaredTrigger[];
  value: string;
  shared: string;
  onChange: (connectionId: string) => void;
  onConnect: () => void;
}) {
  const { entry } = useIntegrationEntry(integration, declared);
  if (!entry) return <p className={hintClass}>Uses your default {integration} connection.</p>;
  return <ConnectionPicker entry={entry} value={value || shared} onChange={onChange} onConnect={onConnect} />;
}
