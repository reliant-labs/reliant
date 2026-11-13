// Copyright (c) 2025 Reliant Labs

/**
 * Adopting an automation a former member left: as an admin taking it
 * yourself (AdoptTrigger), or as the member it was offered to
 * (AcceptTriggerOffer). Either way the new owner chooses everything that
 * was the old owner's: their project in this org, their machine, and a new
 * signing secret for a signed webhook, and their own value for each input it
 * sets (the old values are never shown or kept). Nothing of the old owner's is carried,
 * and the automation stays paused until its new owner turns it on.
 */

import { useId, useMemo, useState, type FormEvent } from "react";
import { create, fromJson, type JsonValue } from "@bufbuild/protobuf";
import { ValueSchema, type Value } from "@bufbuild/protobuf/wkt";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import type { Trigger as ProtoTrigger } from "@/gen/reliant/v1/trigger_pb";
import { useAcceptTriggerOffer, useAdoptTrigger } from "@/hooks/automation-takeover-service-hooks_gen";
import { useProjectDaemonInstalls, useProjects } from "@/hooks/project-queries";
import { jsToProtoValue } from "@/api/proto-utils";
import { triggerFromProto } from "@/hooks/trigger-model";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { declaredTriggers } from "@/lib/declaredTriggers";
import { inputsMappedByEveryTrigger } from "@/lib/setupInputs";
import { cn } from "@/lib/utils";

import { buildDaemonChoices, NO_MACHINE, NO_MACHINE_LABEL } from "../../../Automations/daemonChoices";
import { WebhookCredentialPanel } from "../../../Automations/dialog/WebhookCredentialPanel";
import { useWorkflowDefinition } from "../../../workflow/useWorkflowDefinition";
import { RunWorkflowForm, type RunWorkflowFormStatus } from "../../../workflow/run/RunWorkflowForm";
import { EMPTY_RUN_VALUE, type RunWorkflowValue } from "../../../workflow/run/runWorkflowValues";
import { needsSigningSecret, SELECT_CLASS, sourceLabel, takeoverErrorMessage } from "./takeover";

/** paramNames are the inputs the trigger sets; their old values are withheld. */
export type AdoptTarget =
  | { kind: "adopt"; trigger: ProtoTrigger; paramNames: string[] }
  | { kind: "accept"; offerId: string; trigger: ProtoTrigger; paramNames: string[] };

interface Result {
  trigger?: ProtoTrigger;
  webhook?: { token: string; url: string };
  notices: string[];
}

export function AdoptAutomationDialog({
  orgId,
  target,
  onClose,
  onDone,
}: {
  orgId: string;
  target: AdoptTarget | null;
  onClose: () => void;
  onDone?: () => void;
}) {
  return (
    <Modal open={target !== null} onClose={onClose} title={target ? `Adopt “${target.trigger.name}”` : undefined} size="md">
      {target && <AdoptForm key={target.trigger.id} orgId={orgId} target={target} onClose={onClose} onDone={onDone} />}
    </Modal>
  );
}

function AdoptForm({ orgId, target, onClose, onDone }: { orgId: string; target: AdoptTarget; onClose: () => void; onDone?: () => void }) {
  const projectFieldId = useId();
  const machineFieldId = useId();
  const secretFieldId = useId();
  const { projects } = useProjects();
  const orgProjects = useMemo(() => projects.filter((p) => p.org_id === orgId), [projects, orgId]);
  const [projectId, setProjectId] = useState(orgProjects[0]?.id ?? "");
  const { daemons } = useDaemonStatus();
  const installs = useProjectDaemonInstalls(!!projectId);
  const choices = useMemo(() => buildDaemonChoices(daemons, installs.data ?? [], projectId), [daemons, installs.data, projectId]);
  const [machine, setMachine] = useState("");
  const chosenMachine = machine || choices.find((c) => c.eligible)?.daemonId || "";
  const [secret, setSecret] = useState("");
  const [params, setParams] = useState<Record<string, ParamInput>>({});
  const encoded = useMemo(() => encodeParams(params), [params]);
  // The workflow's own typed inputs, as the adopter's project resolves it:
  // the same form (and the same value encoding) creating an automation uses.
  // Only when it does not resolve there (the departed owner's own saved
  // workflow, not forked yet) does the dialog fall back to typing each
  // input by hand.
  const workflowRef = target.trigger.workflow;
  const definition = useWorkflowDefinition(projectId || undefined, workflowRef);
  const schemaKnown = !!workflowRef && !!definition.workflowDef && !definition.error;
  const [runValue, setRunValue] = useState<RunWorkflowValue>(EMPTY_RUN_VALUE);
  const [runStatus, setRunStatus] = useState<RunWorkflowFormStatus | null>(null);
  // An activation's inputs its declaration maps from each event are the
  // event's, never the adopter's (the server refuses them).
  const mapped = useMemo(() => {
    if (!definition.workflowDef || !target.trigger.workflowTrigger) return [];
    return inputsMappedByEveryTrigger(declaredTriggers(definition.workflowDef).filter((d) => d.name === target.trigger.workflowTrigger));
  }, [definition.workflowDef, target.trigger.workflowTrigger]);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<Result | null>(null);
  const adopt = useAdoptTrigger({ meta: { silenceErrorToast: true } });
  const accept = useAcceptTriggerOffer({ meta: { silenceErrorToast: true } });
  const busy = adopt.isPending || accept.isPending;
  const wantsSecret = needsSigningSecret(target.trigger);

  if (result) {
    const adopted = result.trigger ? triggerFromProto(result.trigger) : null;
    return (
      <div className="space-y-4" data-testid="adopt-result">
        <p className="text-sm text-foreground">
          “{target.trigger.name}” is yours now. It is paused: turn it on from Automations when you are ready.
        </p>
        {result.notices.length > 0 && (
          <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground" data-testid="adopt-notices">
            {result.notices.map((n) => (
              <li key={n}>{n}</li>
            ))}
          </ul>
        )}
        {adopted && result.webhook && <WebhookCredentialPanel trigger={adopted} credential={result.webhook} />}
        <div className="flex justify-end">
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        </div>
      </div>
    );
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    const rebinding = {
      projectId,
      daemonId: chosenMachine === NO_MACHINE ? "" : chosenMachine,
      noMachine: chosenMachine === NO_MACHINE,
      webhookHmacSecret: wantsSecret ? secret : undefined,
      // Only what the adopter set. With the workflow's schema, each value is
      // its declared type (RunWorkflowForm), encoded exactly as a create
      // encodes it; without one, the type the adopter chose. The server
      // validates each against the workflow's inputs as theirs, and names one
      // it refuses.
      params: schemaKnown
        ? Object.fromEntries(Object.entries(runValue.params).map(([k, v]) => [k, jsToProtoValue(v)]))
        : encoded.values,
      presets: schemaKnown ? Object.fromEntries(Object.entries(runValue.presets).filter(([, v]) => !!v)) : {},
    };
    try {
      const resp =
        target.kind === "adopt"
          ? await adopt.mutateAsync({ triggerId: target.trigger.id, rebinding })
          : await accept.mutateAsync({ offerId: target.offerId, rebinding });
      setResult({ trigger: resp.trigger, webhook: resp.webhook, notices: resp.notices });
      onDone?.();
    } catch (err) {
      setError(takeoverErrorMessage(err, `Could not adopt “${target.trigger.name}”.`));
    }
  };

  const inputsReady = schemaKnown
    ? !!runStatus && !runStatus.loading && runStatus.missingRequired.length === 0
    : !definition.loading && Object.keys(encoded.errors).length === 0;
  const ready = !!projectId && !!chosenMachine && (!wantsSecret || secret.trim() !== "") && inputsReady;

  return (
    <form className="space-y-4" onSubmit={(e) => void submit(e)} data-testid="adopt-form">
      <p className="text-sm text-muted-foreground">
        {sourceLabel(target.trigger)}. Its runs will act as you: on your machine, in your project, with your
        connections and access. Nothing of its previous owner&apos;s is kept.
      </p>
      <div className="space-y-1.5">
        <label htmlFor={projectFieldId} className="block text-sm font-medium text-foreground">
          Project
        </label>
        {orgProjects.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="adopt-no-projects">
            You have no project in this organization yet. Add one, then adopt it.
          </p>
        ) : (
          <select
            id={projectFieldId}
            className={SELECT_CLASS}
            value={projectId}
            onChange={(e) => {
              setProjectId(e.target.value);
              setMachine("");
              // The workflow, its inputs and its presets are the project's: a
              // value or preset chosen against the last project is not one
              // this project offers, so the inputs start again (and the
              // typed form remounts on the project, applying its defaults).
              setRunValue(EMPTY_RUN_VALUE);
              setRunStatus(null);
              setParams({});
            }}
            data-testid="adopt-project"
          >
            {orgProjects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        )}
      </div>
      <div className="space-y-1.5">
        <label htmlFor={machineFieldId} className="block text-sm font-medium text-foreground">
          Machine
        </label>
        <select id={machineFieldId} className={SELECT_CLASS} value={chosenMachine} onChange={(e) => setMachine(e.target.value)} data-testid="adopt-machine">
          {chosenMachine === "" && <option value="">Choose a machine</option>}
          {choices.map((c) => (
            <option key={c.daemonId} value={c.daemonId} disabled={!c.eligible}>
              {c.label} · {c.eligible ? c.statusLabel : c.ineligibleReason}
            </option>
          ))}
          <option value={NO_MACHINE}>{NO_MACHINE_LABEL}</option>
        </select>
      </div>
      {wantsSecret && (
        <div className="space-y-1.5">
          <label htmlFor={secretFieldId} className="block text-sm font-medium text-foreground">
            New signing secret
          </label>
          <Input id={secretFieldId} type="password" value={secret} onChange={(e) => setSecret(e.target.value)} autoComplete="off" data-testid="adopt-secret" />
          <p className="text-xs text-muted-foreground">The previous owner&apos;s secret is not carried. Give the sender this one too.</p>
        </div>
      )}
      {schemaKnown ? (
        <div className="space-y-2" data-testid="adopt-typed-inputs">
          <p className="text-sm font-medium text-foreground">Inputs</p>
          <p className="text-xs text-muted-foreground">
            Its previous owner&apos;s values are not carried: they may have been their tokens or addresses. Set your own.
          </p>
          <RunWorkflowForm
            key={projectId}
            projectId={projectId}
            workflowRef={workflowRef}
            value={runValue}
            onChange={setRunValue}
            onStatusChange={setRunStatus}
            showValidation
            applyDefaultPresets
            excludeInputs={mapped}
            hideWorktree
            hideInputsLabel
          />
        </div>
      ) : definition.loading && !!workflowRef ? (
        <p className="text-sm text-muted-foreground" role="status">
          Loading its inputs…
        </p>
      ) : target.paramNames.length > 0 && (
        <fieldset className="space-y-3" data-testid="adopt-params">
          <legend className="text-sm font-medium text-foreground">Inputs</legend>
          <p className="text-xs text-muted-foreground">
            Its previous owner&apos;s values are not carried: they may have been their tokens or addresses. Its workflow
            could not be read in this project, so enter your own by type, or leave one blank.
          </p>
          {target.paramNames.map((name) => (
            <ParamField
              key={name}
              name={name}
              value={params[name] ?? EMPTY_INPUT}
              error={encoded.errors[name]}
              onChange={(v) => setParams((p) => ({ ...p, [name]: v }))}
            />
          ))}
        </fieldset>
      )}
      {error && (
        <p role="alert" className="rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm text-destructive-ink" data-testid="adopt-error">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" type="button" onClick={onClose} disabled={busy}>
          Cancel
        </Button>
        <Button variant="primary" type="submit" loading={busy} disabled={!ready || busy} data-testid="adopt-submit">
          Adopt
        </Button>
      </div>
    </form>
  );
}

/** One input's value as the adopter typed it, and the type it is sent as. */
export type ParamKind = "text" | "number" | "boolean" | "json";
export interface ParamInput {
  kind: ParamKind;
  text: string;
}
const EMPTY_INPUT: ParamInput = { kind: "text", text: "" };

const KIND_LABEL: Record<ParamKind, string> = { text: "Text", number: "Number", boolean: "Yes / no", json: "JSON" };

/**
 * The inputs as protobuf Values of the type each was given: an input is not
 * always a string (a count, a flag, a list of repositories), and sending
 * "3" for a number would fail the workflow's input check. Blank inputs are
 * left out; one that does not parse as its type is an error, not sent.
 */
export function encodeParams(params: Record<string, ParamInput>): { values: Record<string, Value>; errors: Record<string, string> } {
  const values: Record<string, Value> = {};
  const errors: Record<string, string> = {};
  for (const [name, { kind, text }] of Object.entries(params)) {
    const raw = text.trim();
    if (raw === "") continue;
    switch (kind) {
      case "text":
        values[name] = create(ValueSchema, { kind: { case: "stringValue", value: text } });
        break;
      case "number": {
        const n = Number(raw);
        if (Number.isFinite(n)) values[name] = create(ValueSchema, { kind: { case: "numberValue", value: n } });
        else errors[name] = "Not a number.";
        break;
      }
      case "boolean":
        if (raw === "true" || raw === "false") values[name] = create(ValueSchema, { kind: { case: "boolValue", value: raw === "true" } });
        else errors[name] = "Choose yes or no.";
        break;
      case "json":
        try {
          values[name] = fromJson(ValueSchema, JSON.parse(raw) as JsonValue);
        } catch {
          errors[name] = "Not valid JSON.";
        }
        break;
    }
  }
  return { values, errors };
}

function ParamField({ name, value, error, onChange }: { name: string; value: ParamInput; error?: string; onChange: (v: ParamInput) => void }) {
  const id = useId();
  const kindId = useId();
  const errorId = useId();
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-xs font-medium text-foreground">
        {name}
      </label>
      <div className="flex gap-2">
        {value.kind === "boolean" ? (
          <select id={id} className={SELECT_CLASS} value={value.text} onChange={(e) => onChange({ ...value, text: e.target.value })} data-testid={`adopt-param-${name}`}>
            <option value="">Not set</option>
            <option value="true">Yes</option>
            <option value="false">No</option>
          </select>
        ) : (
          <Input
            id={id}
            value={value.text}
            onChange={(e) => onChange({ ...value, text: e.target.value })}
            autoComplete="off"
            inputMode={value.kind === "number" ? "decimal" : undefined}
            aria-invalid={!!error}
            aria-describedby={error ? errorId : undefined}
            data-testid={`adopt-param-${name}`}
          />
        )}
        <label htmlFor={kindId} className="sr-only">
          {name} type
        </label>
        <select
          id={kindId}
          className={cn(SELECT_CLASS, "w-28 shrink-0")}
          value={value.kind}
          onChange={(e) => onChange({ kind: e.target.value as ParamKind, text: "" })}
          data-testid={`adopt-param-kind-${name}`}
        >
          {(Object.keys(KIND_LABEL) as ParamKind[]).map((k) => (
            <option key={k} value={k}>
              {KIND_LABEL[k]}
            </option>
          ))}
        </select>
      </div>
      {error && (
        <p id={errorId} className="text-xs text-destructive-ink" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
