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
import { create } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import type { Trigger as ProtoTrigger } from "@/gen/reliant/v1/trigger_pb";
import { useAcceptTriggerOffer, useAdoptTrigger } from "@/hooks/automation-takeover-service-hooks_gen";
import { useProjectDaemonInstalls, useProjects } from "@/hooks/project-queries";
import { triggerFromProto } from "@/hooks/trigger-model";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";

import { buildDaemonChoices, NO_MACHINE, NO_MACHINE_LABEL } from "../../../Automations/daemonChoices";
import { WebhookCredentialPanel } from "../../../Automations/dialog/WebhookCredentialPanel";
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
  const [params, setParams] = useState<Record<string, string>>({});
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
      // Only what the adopter typed; an input left blank is not sent.
      params: Object.fromEntries(
        Object.entries(params)
          .filter(([, v]) => v.trim() !== "")
          .map(([k, v]) => [k, create(ValueSchema, { kind: { case: "stringValue", value: v } })]),
      ),
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

  const ready = !!projectId && !!chosenMachine && (!wantsSecret || secret.trim() !== "");

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
      {target.paramNames.length > 0 && (
        <fieldset className="space-y-3" data-testid="adopt-params">
          <legend className="text-sm font-medium text-foreground">Inputs</legend>
          <p className="text-xs text-muted-foreground">
            Its previous owner&apos;s values are not carried: they may have been their tokens or addresses. Enter your own, or leave one blank.
          </p>
          {target.paramNames.map((name) => (
            <ParamField key={name} name={name} value={params[name] ?? ""} onChange={(v) => setParams((p) => ({ ...p, [name]: v }))} />
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

function ParamField({ name, value, onChange }: { name: string; value: string; onChange: (v: string) => void }) {
  const id = useId();
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-xs font-medium text-foreground">
        {name}
      </label>
      <Input id={id} value={value} onChange={(e) => onChange(e.target.value)} autoComplete="off" data-testid={`adopt-param-${name}`} />
    </div>
  );
}
