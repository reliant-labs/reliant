// Copyright (c) 2025 Reliant Labs

/**
 * Activate a workflow's declared trigger: the AS WHOM / WHERE half
 * (research/INTEGRATIONS_V1_BRIEF.md §3a). The declaration fixes WHEN — its
 * source, filter and inputs — so this asks only for what belongs to the
 * person activating it:
 *
 *   project · workspace · machine (or "No machine") · connection
 *   (integration triggers) · prompt · the inputs the declaration does not
 *   map · notify · enabled
 *
 * It creates a trigger row with the `workflow_trigger` arm. Inputs the
 * declaration maps are listed read-only and never sent as params: the server
 * refuses them, because the event would overwrite them on every fire.
 *
 * A webhook trigger's URL and token come back from the create, once; they
 * are shown here with copy, and can be rotated later from the same screen.
 */

import { useEffect, useId, useMemo, useState, type FormEvent } from "react";
import { Check, Copy, KeyRound, RotateCw } from "lucide-react";

import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { CardInset } from "../forge-ui/card";
import { Toggle } from "../ui/Toggle";
import { useProjectStore } from "@/store/projectStore";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { useProjectDaemonInstalls, triggerKeys } from "@/hooks/trigger-queries";
import { useQueryClient } from "@tanstack/react-query";
import {
  triggerErrorMessage,
  triggerGrpc,
  webhookUrlForDisplay,
  type Trigger,
  type WebhookCredential,
} from "@/api/trigger-grpc";
import { getGRPCBaseURLPublic } from "@/api/grpc-client";
import { RunWorkflowForm, type RunWorkflowFormStatus } from "../workflow/run/RunWorkflowForm";
import type { RunWorkflowValue } from "../workflow/run/runWorkflowValues";
import { fieldClass, hintClass, labelClass, textareaClass, errorTextClass } from "../workflow/run/runFormStyles";
import { buildDaemonChoices, defaultDaemonId } from "./daemonChoices";
import { ConnectionPicker } from "../workflow/connections/ConnectionPicker";
import { ConnectIntegrationDialog } from "../workflow/connections/ConnectIntegrationDialog";
import { useCatalogEntry, useDeclaredTriggerRef } from "@/hooks/connection-queries";
import { integrationOf, sourceCase, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { cn } from "@/lib/utils";

export interface ActivateTriggerDialogProps {
  open: boolean;
  onClose: () => void;
  /** The stored workflow ref the trigger is declared in. */
  workflowRef: string;
  workflowTitle?: string;
  declared: DeclaredTrigger;
  /** A catalog ref of the trigger type, for its connection methods. */
  catalogRef?: string;
  defaultProjectId?: string;
  onActivated?: (trigger: Trigger) => void;
}

const NO_MACHINE = "__no_machine__";

/** The API origin a bare `/hooks/…` webhook path is relative to. */
export function apiOrigin(): string {
  return getGRPCBaseURLPublic() ?? (typeof window !== "undefined" ? window.location.origin : "");
}

export function ActivateTriggerDialog(props: ActivateTriggerDialogProps) {
  if (!props.open) return null;
  return <ActivateBody {...props} />;
}

function ActivateBody({ onClose, workflowRef, workflowTitle, declared, catalogRef, defaultProjectId, onActivated }: ActivateTriggerDialogProps) {
  const ids = useId();
  const fieldId = (name: string) => `${ids}-${name}`;
  const queryClient = useQueryClient();
  const projects = useProjectStore((s) => s.projects);
  const currentProjectId = useProjectStore((s) => s.currentProject?.id);
  const kind = sourceCase(declared);
  const integration = integrationOf(declared);
  const mappedInputs = Object.keys(declared.inputs ?? {});

  const [name, setName] = useState(`${workflowTitle || workflowRef} · ${declared.name}`);
  const [projectId, setProjectId] = useState(defaultProjectId ?? currentProjectId ?? projects[0]?.id ?? "");
  const [inputs, setInputs] = useState<RunWorkflowValue>({ presets: {}, params: {} });
  const [inputsStatus, setInputsStatus] = useState<RunWorkflowFormStatus | null>(null);
  const [machine, setMachine] = useState("");
  const [machineChosen, setMachineChosen] = useState(false);
  const [connectionId, setConnectionId] = useState("");
  const [connectOpen, setConnectOpen] = useState(false);
  const [message, setMessage] = useState(declared.description ? `${declared.description}.` : "");
  const [enabled, setEnabled] = useState(true);
  const [notify, setNotify] = useState(false);
  const [attempted, setAttempted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [created, setCreated] = useState<{ trigger: Trigger; webhook?: WebhookCredential } | null>(null);

  const { daemons, loading: daemonsLoading } = useDaemonStatus();
  const installs = useProjectDaemonInstalls();
  const choices = useMemo(() => buildDaemonChoices(daemons, installs.data ?? [], projectId), [daemons, installs.data, projectId]);
  useEffect(() => {
    if (machineChosen || daemonsLoading || installs.isLoading) return;
    setMachine(defaultDaemonId(choices) ?? "");
  }, [choices, machineChosen, daemonsLoading, installs.isLoading]);

  const resolvedRef = useDeclaredTriggerRef(kind === "integration" ? integration?.integration : undefined, integration?.events ?? [], catalogRef);
  const entry = useCatalogEntry(kind === "integration" ? resolvedRef : undefined).data;

  if (created) {
    return <ActivatedView created={created} onClose={onClose} />;
  }

  const errors: Record<string, string> = {};
  if (!name.trim()) errors.name = "Name the activation.";
  if (!projectId) errors.project = "Choose a project.";
  if (!machine) errors.machine = daemons.length === 0 ? "Connect a machine first, or choose No machine." : "Choose where its runs execute.";
  if (!message.trim()) errors.message = "Write the prompt each run starts from.";
  if (inputsStatus?.loading) errors.inputs = "Wait for this workflow's inputs to load.";
  else if (inputsStatus?.missingRequired.length) errors.inputs = `Fill in: ${inputsStatus.missingRequired.join(", ")}.`;

  const onSubmit = async (event: FormEvent) => {
    event.preventDefault();
    setAttempted(true);
    setError(null);
    if (Object.keys(errors).length > 0) return;
    setSaving(true);
    try {
      const result = await triggerGrpc.createWithCredential({
        name: name.trim(),
        projectId,
        worktreeId: inputs.worktreeId,
        enabled,
        workflow: workflowRef,
        presets: inputs.presets,
        params: inputs.params,
        message: message.trim(),
        daemonId: machine === NO_MACHINE ? "" : machine,
        noMachine: machine === NO_MACHINE,
        notifyOnComplete: notify,
        connectionId: connectionId || undefined,
        source: { kind: "activation", workflowTrigger: declared.name ?? "" },
      });
      void queryClient.invalidateQueries({ queryKey: triggerKeys.lists() });
      onActivated?.(result.trigger);
      if (result.webhook || kind === "webhook") setCreated(result);
      else onClose();
    } catch (err) {
      setError(triggerErrorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  const show = (key: string) => attempted && errors[key];

  return (
    <Modal isOpen onClose={onClose} size="lg" title={`Activate “${declared.name}”`}>
      <form onSubmit={onSubmit} noValidate className="space-y-5" aria-label={`Activate ${declared.name}`}>
        <CardInset className="text-sm">
          <p className="text-foreground">
            Runs <span className="font-medium">{workflowTitle || workflowRef}</span> when its <span className="font-mono">{declared.name}</span> trigger fires.
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            When it fires, its filter and its input mapping come from the workflow. Choose where the runs execute and as whom.
          </p>
        </CardInset>

        <div>
          <label htmlFor={fieldId("name")} className={labelClass}>Name</label>
          <input id={fieldId("name")} className={fieldClass} value={name} onChange={(e) => setName(e.target.value)} aria-invalid={!!show("name")} />
          {show("name") && <p className={errorTextClass}>{errors.name}</p>}
        </div>

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
            <select
              id={fieldId("machine")}
              className={fieldClass}
              value={machine}
              aria-describedby={fieldId("machine-hint")}
              onChange={(e) => {
                setMachine(e.target.value);
                setMachineChosen(true);
              }}
              aria-invalid={!!show("machine")}
            >
              <option value="">{daemonsLoading ? "Loading machines…" : "Choose a machine"}</option>
              {choices.map((choice) => (
                <option key={choice.daemonId} value={choice.daemonId} disabled={!choice.eligible}>
                  {choice.label} ({choice.statusLabel}{choice.ineligibleReason ? `, ${choice.ineligibleReason}` : ""})
                </option>
              ))}
              <option value={NO_MACHINE}>
                No machine (server tools only)
              </option>
            </select>
            <p id={fieldId("machine-hint")} className={hintClass}>
              {machine === NO_MACHINE
                ? "Runs use only web, integration and planning tools; nothing touches files."
                : "Every tool call in each run executes on this machine."}
            </p>
            {show("machine") && <p className={errorTextClass}>{errors.machine}</p>}
          </div>
        </div>

        {kind === "integration" && integration && entry && (
          <div>
            <ConnectionPicker entry={entry} value={connectionId} onChange={setConnectionId} onConnect={() => setConnectOpen(true)} />
            <p className={hintClass}>Events reach this activation only through the connection's account.</p>
          </div>
        )}

        <div>
          <label htmlFor={fieldId("message")} className={labelClass}>Prompt</label>
          <textarea
            id={fieldId("message")}
            className={textareaClass}
            rows={3}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder="Triage the new issue: label it, and ask for a repro if one is missing."
            aria-invalid={!!show("message")}
          />
          <p className={hintClass}>Each run starts from this message. Nobody will be watching, so say everything the agent needs.</p>
          {show("message") && <p className={errorTextClass}>{errors.message}</p>}
        </div>

        {mappedInputs.length > 0 && (
          <div>
            <span className={labelClass}>Set from each event</span>
            <CardInset padding="none">
              <ul className="divide-y divide-border/60 text-xs">
                {mappedInputs.map((input) => (
                  <li key={input} className="flex items-baseline gap-3 px-3 py-1.5">
                    <span className="w-32 flex-shrink-0 font-mono text-foreground">{input}</span>
                    <span className="min-w-0 truncate font-mono text-muted-foreground">{(declared.inputs as Record<string, string>)[input]}</span>
                  </li>
                ))}
              </ul>
            </CardInset>
            <p className={hintClass}>The workflow's trigger maps these; edit them in the workflow.</p>
          </div>
        )}

        {projectId && (
          <RunWorkflowForm
            projectId={projectId}
            workflowRef={workflowRef}
            value={inputs}
            onChange={setInputs}
            onStatusChange={setInputsStatus}
            showValidation={attempted}
            applyDefaultPresets
            excludeInputs={mappedInputs}
          />
        )}
        {show("inputs") && !inputsStatus?.missingRequired.length && <p className={errorTextClass} role="alert">{errors.inputs}</p>}

        <div className="flex flex-wrap items-center gap-6">
          <div className="flex items-center gap-2">
            <Toggle checked={enabled} onChange={setEnabled} srLabel="Enabled" />
            <span className="text-sm text-foreground" aria-hidden>Enabled</span>
          </div>
          <div className="flex items-center gap-2">
            <Toggle checked={notify} onChange={setNotify} srLabel="Notify me when a run completes" />
            <span className="text-sm text-foreground" aria-hidden>Notify me when a run completes</span>
          </div>
        </div>

        {error && (
          <p role="alert" className="rounded-md border border-danger-border bg-danger-surface px-3 py-2 text-sm text-danger-ink">
            {error}
          </p>
        )}

        <div className="flex justify-end gap-2 border-t border-border pt-4">
          <Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" loading={saving}>Activate</Button>
        </div>
      </form>

      {integration && (
        <ConnectIntegrationDialog
          target={connectOpen && resolvedRef ? { ref: resolvedRef, integrationId: integration.integration, displayName: entry?.summary.integration.displayName ?? integration.integration, icon: entry?.summary.integration.icon } : null}
          onClose={() => setConnectOpen(false)}
          onConnected={(connection) => setConnectionId(connection.id)}
        />
      )}
    </Modal>
  );
}

function ActivatedView({ created, onClose }: { created: { trigger: Trigger; webhook?: WebhookCredential }; onClose: () => void }) {
  return (
    <Modal isOpen onClose={onClose} size="lg" title={`${created.trigger.name} is active`}>
      <div className="space-y-4">
        <WebhookCredentialPanel trigger={created.trigger} credential={created.webhook} />
        <div className="flex justify-end border-t border-border pt-4">
          <Button variant="primary" onClick={onClose}>Done</Button>
        </div>
      </div>
    </Modal>
  );
}

/**
 * A webhook trigger's URL, and its token when it was just created or
 * rotated (the only time the server returns it). Rotation invalidates the
 * old token at once.
 */
export function WebhookCredentialPanel({ trigger, credential: initial }: { trigger: Trigger; credential?: WebhookCredential }) {
  const queryClient = useQueryClient();
  const [credential, setCredential] = useState(initial);
  const [rotating, setRotating] = useState(false);
  const [confirmRotate, setConfirmRotate] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const origin = apiOrigin();
  const url = webhookUrlForDisplay(trigger.webhookUrl, origin);
  const tokenUrl = credential ? webhookUrlForDisplay(credential.url, origin) : "";

  const rotate = async () => {
    setRotating(true);
    setError(null);
    try {
      const result = await triggerGrpc.rotateWebhookToken(trigger.id);
      setCredential(result.webhook);
      setConfirmRotate(false);
      void queryClient.invalidateQueries({ queryKey: triggerKeys.all });
    } catch (err) {
      setError(triggerErrorMessage(err));
    } finally {
      setRotating(false);
    }
  };

  return (
    <div className="space-y-3">
      <CopyField label="Webhook URL" value={url} hint="POST here with Authorization: Bearer <token>, or sign the body with your HMAC secret." />
      {credential ? (
        <>
          <CopyField label="Token" value={credential.token} secret hint="Shown once. Store it in the sender now; it can't be shown again, only replaced." />
          {tokenUrl && <CopyField label="URL with token" value={tokenUrl} secret hint="For senders that can't set headers (Zapier). Treat it like the token." />}
        </>
      ) : (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <KeyRound className="h-3.5 w-3.5" aria-hidden /> The token was shown when this was activated. Rotate it to get a new one.
        </p>
      )}
      {confirmRotate ? (
        <div role="alertdialog" aria-label="Rotate the token?" className="rounded-md border border-warning/40 bg-background px-3 py-2 text-sm">
          <p className="text-foreground">Rotate the token? Senders using the current one stop working immediately.</p>
          <div className="mt-2 flex gap-2">
            <Button size="sm" variant="ghost" onClick={() => setConfirmRotate(false)}>Keep it</Button>
            <Button size="sm" variant="destructive" loading={rotating} onClick={() => void rotate()}>Rotate token</Button>
          </div>
        </div>
      ) : (
        <Button size="sm" variant="outline" leftIcon={<RotateCw className="h-3.5 w-3.5" />} onClick={() => setConfirmRotate(true)}>
          Rotate token
        </Button>
      )}
      {error && <p role="alert" className="text-sm text-destructive-ink">{error}</p>}
    </div>
  );
}

function CopyField({ label, value, hint, secret = false }: { label: string; value: string; hint?: string; secret?: boolean }) {
  const id = useId();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // The field stays selectable for a manual copy.
    }
  };
  return (
    <div>
      <label htmlFor={id} className={labelClass}>{label}</label>
      <div className="flex gap-2">
        <input id={id} readOnly value={value} onFocus={(e) => e.currentTarget.select()} className={cn(fieldClass, "font-mono text-xs", secret && "tracking-tight")} />
        <Button type="button" variant="outline" size="sm" onClick={() => void copy()} aria-label={`Copy ${label.toLowerCase()}`} leftIcon={copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}>
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
      {hint && <p className={hintClass}>{hint}</p>}
    </div>
  );
}
