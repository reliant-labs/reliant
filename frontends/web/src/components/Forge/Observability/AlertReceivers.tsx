// Copyright (c) 2025 Reliant Labs

/**
 * Receivers: where an alert's notification goes.
 *
 * A route in the project's code (`forge.AlertRoute`) names a receiver; this is
 * where each NAME gets its destination and secret, because a Slack webhook URL
 * or a PagerDuty routing key does not belong in a repository. So the list says,
 * for each receiver, which environments route to it, and it calls out the
 * opposite gap too: a route naming a receiver this organization has not set up,
 * whose notifications therefore go nowhere.
 *
 * Secrets are write-only: the server says only whether one is stored. The
 * in-app receiver (`reliant-inapp`) exists for every organization and cannot be
 * created, changed or deleted. Changing receivers needs the org admin role.
 */

import { useId, useMemo, useState } from "react";
import { AlertTriangle, Bell, Globe, Lock, Mail, MessageSquare, Pencil, Plus, Send, Siren, Trash2 } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import Modal from "@/components/forge-ui/modal";
import { useAlertingMutation, useReceivers } from "@/hooks/observability-queries";
import type {
  CreateReceiverInput,
  DeclaredAlertRoute,
  Receiver,
  ReceiverType,
  TestReceiverResult,
} from "@/services/controlPlane/observability/alertingTypes";
import { alertingErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import { cn } from "@/lib/utils";

import type { ObsEnvironment } from "./obsPlace";
import { ObsError, ObsLoadingRows } from "./ObsStates";

const FIELD =
  "h-8 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60";

/** The receiver every organization has; notifications land in the console. */
export const INAPP_RECEIVER = "reliant-inapp";

/**
 * Where an environment sends alerts when its code declares no routes: in-app,
 * then Slack — never a pager (forge's ALERT_BUILTIN_RECEIVERS).
 */
export const DEFAULT_ROUTES: DeclaredAlertRoute[] = [
  { match: {}, receiver: INAPP_RECEIVER, continue: true },
  { match: {}, receiver: "slack", continue: false },
];

type CreatableType = CreateReceiverInput["type"];

export const RECEIVER_TYPES: CreatableType[] = ["slack", "email", "webhook", "pagerduty"];

export const RECEIVER_TYPE_LABELS: Record<ReceiverType, string> = {
  slack: "Slack",
  email: "Email",
  webhook: "Webhook",
  pagerduty: "PagerDuty",
  inapp: "In-app",
  unknown: "Unknown",
};

const TYPE_ICON: Record<ReceiverType, typeof Bell> = {
  slack: MessageSquare,
  email: Mail,
  webhook: Globe,
  pagerduty: Siren,
  inapp: Bell,
  unknown: Bell,
};

/** What a type's secret is, in the reader's words; null when the type has none. */
const SECRET_LABEL: Record<CreatableType, { label: string; placeholder: string; required: boolean } | null> = {
  slack: { label: "Incoming webhook URL", placeholder: "https://hooks.slack.com/services/…", required: true },
  pagerduty: { label: "Events v2 routing key", placeholder: "32-character integration key", required: true },
  webhook: { label: "Bearer token (optional)", placeholder: "Sent as Authorization: Bearer …", required: false },
  email: null,
};

/**
 * The routes an environment actually runs. A recorded shape lists them; one
 * that declares rules but no routes is on forge's defaults. An environment with
 * nothing recorded, or with observability off, routes nothing.
 */
export function effectiveRoutes(env: ObsEnvironment): DeclaredAlertRoute[] {
  const declared = env.declared;
  if (!declared || declared.observability?.enabled === false) return [];
  if (declared.alertRoutes?.length) return declared.alertRoutes;
  return declared.alerts?.length ? DEFAULT_ROUTES : [];
}

/** Receiver name → the environments whose routes name it, in the order given. */
export function receiverRouting(environments: ObsEnvironment[]): Map<string, ObsEnvironment[]> {
  const out = new Map<string, ObsEnvironment[]>();
  for (const env of environments) {
    for (const name of new Set(effectiveRoutes(env).map((r) => r.receiver))) {
      out.set(name, [...(out.get(name) ?? []), env]);
    }
  }
  return out;
}

/** The non-secret settings, as one line. */
export function settingsSummary(receiver: Pick<Receiver, "type" | "settings">): string {
  const s = receiver.settings;
  switch (receiver.type) {
    case "slack":
      return s.channel ? s.channel : "the webhook's default channel";
    case "email":
      return s.to ?? "";
    case "webhook":
      return s.url ?? "";
    case "inapp":
      return "Notifications in the Reliant console";
    default:
      return "";
  }
}

export interface ReceiverForm {
  name: string;
  type: CreatableType;
  /** Slack channel, email recipients or webhook URL, by type. */
  target: string;
  secret: string;
}

/** The settings key a type's `target` is stored under. */
const TARGET_KEY: Record<CreatableType, string | null> = { slack: "channel", email: "to", webhook: "url", pagerduty: null };

function isPublicHttps(raw: string): boolean {
  try {
    const u = new URL(raw);
    if (u.protocol !== "https:" || !u.hostname) return false;
    const h = u.hostname.toLowerCase();
    return !(h === "localhost" || h.endsWith(".internal") || h.endsWith(".local") || h.endsWith(".svc") || h.includes(".svc."));
  } catch {
    return false;
  }
}

/**
 * What is wrong with a receiver before it is sent, in words — the server's own
 * rules (control-plane validateReceiver and the name pattern), checked first so
 * the reader is not sent a round trip to learn them.
 */
export function receiverProblems(form: ReceiverForm, mode: "create" | "edit", existing: string[] = []): string[] {
  const problems: string[] = [];
  if (mode === "create") {
    if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$/.test(form.name)) {
      problems.push("A name is up to 63 letters, digits, '.', '_' or '-', starting with a letter or digit: it is what a route in your code names.");
    } else if (form.name === INAPP_RECEIVER || existing.includes(form.name)) {
      problems.push(`A receiver named "${form.name}" already exists.`);
    }
  }
  const target = form.target.trim();
  const secret = form.secret.trim();
  switch (form.type) {
    case "slack":
      if (mode === "create" && !secret) problems.push("Paste the Slack incoming-webhook URL.");
      else if (secret && !isPublicHttps(secret)) problems.push("The Slack webhook must be an https URL.");
      break;
    case "pagerduty":
      if (mode === "create" && !secret) problems.push("Paste the PagerDuty Events v2 routing key.");
      break;
    case "webhook":
      if (!isPublicHttps(target)) problems.push("The webhook URL must be https, on a public host.");
      break;
    case "email":
      if (!target) problems.push("Add at least one address.");
      else if (target.split(",").some((a) => !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(a.trim()))) {
        problems.push("Separate addresses with commas, each like team@example.com.");
      }
      break;
  }
  return problems;
}

/** The settings map to send: the receiver's current settings with this form's target applied. */
export function formSettings(form: ReceiverForm, current: Record<string, string> = {}): Record<string, string> {
  const key = TARGET_KEY[form.type];
  const out = { ...current };
  if (!key) return out;
  const target = form.target.trim();
  if (target) out[key] = target;
  else delete out[key];
  return out;
}

export interface AlertReceiversProps {
  orgId: string;
  /** Every environment of the place, for "who routes here" — not only the ones in view. */
  environments: ObsEnvironment[];
  readOnly: boolean;
  onDenied: () => void;
}

type Editing = { mode: "create"; name?: string } | { mode: "edit"; receiver: Receiver };

export function AlertReceivers({ orgId, environments, readOnly, onDenied }: AlertReceiversProps) {
  const receivers = useReceivers(orgId);
  const [editing, setEditing] = useState<Editing | null>(null);
  const routing = useMemo(() => receiverRouting(environments), [environments]);

  if (receivers.isLoading) return <ObsLoadingRows testId="obs-receivers-loading" />;
  if (receivers.error) {
    return <ObsError error={receivers.error} subject="this organization" onRetry={() => void receivers.refetch()} retrying={receivers.isFetching} />;
  }
  const list = [...(receivers.data ?? [])].sort((a, b) =>
    a.type === "inapp" ? -1 : b.type === "inapp" ? 1 : a.name.localeCompare(b.name)
  );
  const names = new Set(list.map((r) => r.name));
  const missing = [...routing.entries()].filter(([name]) => !names.has(name) && name !== INAPP_RECEIVER);

  return (
    <div className="space-y-3" data-testid="obs-receivers">
      <div className="flex flex-wrap items-center gap-3">
        <Button
          variant="primary"
          size="sm"
          leftIcon={<Plus className="h-3.5 w-3.5" />}
          disabled={readOnly}
          onClick={() => setEditing({ mode: "create" })}
          data-testid="obs-receiver-new"
        >
          New receiver
        </Button>
        <p className="text-xs text-muted-foreground">
          Routes in your code name a receiver (<code className="font-mono">forge.AlertRoute.receiver</code>); this is where each name gets its
          destination.
        </p>
      </div>

      {missing.length > 0 && (
        <div className="space-y-2 rounded-lg border border-warning/40 bg-background px-4 py-3" role="status" data-testid="obs-receivers-missing">
          <p className="flex items-center gap-1.5 text-sm font-medium text-foreground">
            <AlertTriangle className="h-4 w-4 text-warning" aria-hidden="true" />
            {missing.length === 1 ? "A route names a receiver" : `${missing.length} routes name receivers`} that isn&apos;t set up
          </p>
          <ul className="space-y-1.5">
            {missing.map(([name, envs]) => (
              <li key={name} className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground" data-testid={`obs-receiver-missing-${name}`}>
                <span>
                  <EnvList envs={envs} /> {envs.length === 1 ? "sends" : "send"} alerts to <code className="font-mono text-foreground">{name}</code>,
                  which doesn&apos;t exist, so those notifications go nowhere.
                </span>
                <Button variant="outline" size="xs" disabled={readOnly} onClick={() => setEditing({ mode: "create", name })}>
                  Set up {name}
                </Button>
              </li>
            ))}
          </ul>
        </div>
      )}

      <Card padding="none">
        <ul className="divide-y divide-border/60">
          {list.map((r) => (
            <ReceiverRow
              key={r.id}
              orgId={orgId}
              receiver={r}
              routedFrom={routing.get(r.name) ?? []}
              readOnly={readOnly}
              onDenied={onDenied}
              onEdit={() => setEditing({ mode: "edit", receiver: r })}
            />
          ))}
        </ul>
      </Card>

      {editing && (
        <ReceiverDialog
          orgId={orgId}
          editing={editing}
          existing={list.map((r) => r.name)}
          onClose={() => setEditing(null)}
          onDenied={onDenied}
        />
      )}
    </div>
  );
}

function EnvList({ envs }: { envs: ObsEnvironment[] }) {
  return (
    <>
      {envs.map((env, i) => (
        <span key={env.id}>
          {i > 0 && (i === envs.length - 1 ? " and " : ", ")}
          <span className="font-mono text-foreground">{env.name}</span>
        </span>
      ))}
    </>
  );
}

function ReceiverRow({
  orgId,
  receiver,
  routedFrom,
  readOnly,
  onDenied,
  onEdit,
}: {
  orgId: string;
  receiver: Receiver;
  routedFrom: ObsEnvironment[];
  readOnly: boolean;
  onDenied: () => void;
  onEdit: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [result, setResult] = useState<TestReceiverResult | null>(null);
  const [problem, setProblem] = useState("");
  const test = useAlertingMutation(orgId, (api, org, id: string) => api.testReceiver(org, id));
  const remove = useAlertingMutation(orgId, (api, org, id: string) => api.deleteReceiver(org, id));
  const Icon = TYPE_ICON[receiver.type];
  const builtIn = receiver.type === "inapp";
  const fail = (error: Error, what: string) => {
    const kind = alertingErrorKind(error);
    if (kind === "denied") {
      onDenied();
      setProblem(`${what} needs the organization admin role.`);
    } else if (kind === "in-use") {
      setProblem(
        `A route in the code still names ${receiver.name}${routedFrom.length ? ` (${routedFrom.map((e) => e.name).join(", ")})` : ""}. Remove the route and deploy, then delete it.`
      );
    } else setProblem(obsErrorMessage(error) || `${what} failed.`);
  };

  return (
    <li className="space-y-2 px-4 py-3" data-testid={`obs-receiver-${receiver.name}`} data-type={receiver.type}>
      <div className="flex flex-wrap items-start gap-3">
        <Icon className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-medium text-foreground">{receiver.name}</span>
            <span className="rounded border border-border px-1.5 py-px text-2xs text-muted-foreground">{RECEIVER_TYPE_LABELS[receiver.type]}</span>
            {builtIn ? (
              <span className="text-2xs text-muted-foreground">built in</span>
            ) : receiver.hasSecret ? (
              <span className="inline-flex items-center gap-1 text-2xs text-muted-foreground" data-testid="obs-receiver-secret" title="The secret is stored encrypted and never shown again">
                <Lock className="h-3 w-3" aria-hidden="true" />
                secret set
              </span>
            ) : null}
          </div>
          <p className="break-all text-xs text-foreground">{settingsSummary(receiver)}</p>
          <p className="text-xs text-muted-foreground" data-testid="obs-receiver-routed">
            {routedFrom.length ? (
              <>
                Routed from <EnvList envs={routedFrom} />
              </>
            ) : (
              "No environment routes here yet."
            )}
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            leftIcon={<Send className="h-3.5 w-3.5" />}
            disabled={readOnly || test.isPending}
            loading={test.isPending}
            onClick={() => {
              setProblem("");
              setResult(null);
              test.mutate(receiver.id, { onSuccess: setResult, onError: (e) => fail(e, "Sending a test") });
            }}
            data-testid={`obs-receiver-test-${receiver.name}`}
          >
            Send test
          </Button>
          {!builtIn && (
            <>
              <Button variant="ghost" size="sm" leftIcon={<Pencil className="h-3.5 w-3.5" />} disabled={readOnly} onClick={onEdit} data-testid={`obs-receiver-edit-${receiver.name}`}>
                Edit
              </Button>
              {confirming ? (
                <Button
                  variant="destructive"
                  size="sm"
                  disabled={remove.isPending}
                  loading={remove.isPending}
                  onClick={() =>
                    remove.mutate(receiver.id, {
                      onError: (e) => {
                        setConfirming(false);
                        fail(e, "Deleting a receiver");
                      },
                    })
                  }
                  data-testid={`obs-receiver-delete-confirm-${receiver.name}`}
                >
                  Delete {receiver.name}?
                </Button>
              ) : (
                <Button
                  variant="ghost"
                  size="sm"
                  leftIcon={<Trash2 className="h-3.5 w-3.5" />}
                  disabled={readOnly}
                  onClick={() => {
                    setProblem("");
                    setConfirming(true);
                  }}
                  data-testid={`obs-receiver-delete-${receiver.name}`}
                >
                  Delete
                </Button>
              )}
            </>
          )}
        </div>
      </div>
      {result && <TestResult result={result} receiver={receiver} />}
      {problem && (
        <p role="alert" className="pl-7 text-xs text-destructive" data-testid="obs-receiver-problem">
          {problem}
        </p>
      )}
    </li>
  );
}

function TestResult({ result, receiver }: { result: TestReceiverResult; receiver: Receiver }) {
  const where = receiver.type === "inapp" ? "the notifications list" : settingsSummary(receiver) || receiver.name;
  const [tone, text] = !result.accepted
    ? ["text-destructive", "Alertmanager didn't accept the test alert. Try again in a minute."]
    : !result.receiverLive
      ? [
          "text-warning",
          `Accepted, but ${receiver.name} isn't live yet: Alertmanager picks up a new or changed receiver within about a minute, so this test may not arrive. Send another shortly.`,
        ]
      : ["text-success", `Test sent. It should reach ${where} within a minute.`];
  return (
    <p role="status" className={cn("pl-7 text-xs", tone)} data-testid="obs-receiver-test-result" data-accepted={result.accepted} data-live={result.receiverLive}>
      {text}
    </p>
  );
}

function ReceiverDialog({
  orgId,
  editing,
  existing,
  onClose,
  onDenied,
}: {
  orgId: string;
  editing: Editing;
  existing: string[];
  onClose: () => void;
  onDenied: () => void;
}) {
  const current = editing.mode === "edit" ? editing.receiver : null;
  const initialType: CreatableType = current && current.type !== "inapp" && current.type !== "unknown" ? current.type : "slack";
  const [form, setForm] = useState<ReceiverForm>(() => ({
    name: current?.name ?? (editing.mode === "create" ? (editing.name ?? "") : ""),
    type: initialType,
    target: current ? (current.settings[TARGET_KEY[initialType] ?? ""] ?? "") : "",
    secret: "",
  }));
  const [problems, setProblems] = useState<string[]>([]);
  const create = useAlertingMutation(orgId, (api, org, input: CreateReceiverInput) => api.createReceiver(org, input));
  const update = useAlertingMutation(orgId, (api, org, input: { id: string; settings: Record<string, string>; secret: string }) =>
    api.updateReceiver(org, input)
  );
  const pending = create.isPending || update.isPending;
  const nameId = useId();
  const typeId = useId();
  const targetId = useId();
  const secretId = useId();
  const secret = SECRET_LABEL[form.type];
  const target: { label: string; placeholder: string } | null =
    form.type === "slack"
      ? { label: "Channel (optional)", placeholder: "#alerts — empty uses the webhook's own channel" }
      : form.type === "email"
        ? { label: "To", placeholder: "oncall@example.com, team@example.com" }
        : form.type === "webhook"
          ? { label: "URL", placeholder: "https://example.com/hooks/alerts" }
          : null;
  const set = (patch: Partial<ReceiverForm>) => setForm((prev) => ({ ...prev, ...patch }));

  const onError = (error: Error) => {
    if (alertingErrorKind(error) === "denied") {
      onDenied();
      setProblems(["Changing receivers needs the organization admin role."]);
    } else setProblems([obsErrorMessage(error) || "The receiver couldn't be saved."]);
  };
  const submit = () => {
    const found = receiverProblems(form, editing.mode, existing);
    setProblems(found);
    if (found.length) return;
    if (current) {
      update.mutate({ id: current.id, settings: formSettings(form, current.settings), secret: form.secret.trim() }, { onSuccess: onClose, onError });
    } else {
      create.mutate({ name: form.name, type: form.type, settings: formSettings(form), secret: form.secret.trim() }, { onSuccess: onClose, onError });
    }
  };

  return (
    <Modal open onClose={onClose} title={current ? `Edit ${current.name}` : "New receiver"} size="md">
      <form
        className="space-y-4"
        data-testid="obs-receiver-dialog"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {!current && (
          <div className="grid gap-3 sm:grid-cols-[1fr_auto]">
            <div>
              <label htmlFor={nameId} className="mb-1 block text-xs font-medium text-muted-foreground">
                Name
              </label>
              <input
                id={nameId}
                value={form.name}
                onChange={(e) => set({ name: e.target.value })}
                placeholder="oncall-slack"
                className={cn(FIELD, "font-mono")}
                data-testid="obs-receiver-name"
                autoComplete="off"
              />
            </div>
            <div>
              <label htmlFor={typeId} className="mb-1 block text-xs font-medium text-muted-foreground">
                Type
              </label>
              <select
                id={typeId}
                value={form.type}
                onChange={(e) => set({ type: e.target.value as CreatableType, target: "", secret: "" })}
                className={FIELD}
                data-testid="obs-receiver-type"
              >
                {RECEIVER_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {RECEIVER_TYPE_LABELS[t]}
                  </option>
                ))}
              </select>
            </div>
          </div>
        )}
        {!current && (
          <p className="text-xs text-muted-foreground">
            A route reaches this receiver by name:{" "}
            <code className="font-mono text-foreground">forge.AlertRoute {"{"} receiver = &quot;{form.name || "name"}&quot; {"}"}</code>.
          </p>
        )}
        {target && (
          <div>
            <label htmlFor={targetId} className="mb-1 block text-xs font-medium text-muted-foreground">
              {target.label}
            </label>
            <input
              id={targetId}
              value={form.target}
              onChange={(e) => set({ target: e.target.value })}
              placeholder={target.placeholder}
              className={FIELD}
              data-testid="obs-receiver-target"
              autoComplete="off"
            />
          </div>
        )}
        {secret && (
          <div>
            <label htmlFor={secretId} className="mb-1 block text-xs font-medium text-muted-foreground">
              {secret.label}
            </label>
            <input
              id={secretId}
              type="password"
              value={form.secret}
              onChange={(e) => set({ secret: e.target.value })}
              placeholder={current?.hasSecret ? "Stored — leave empty to keep it, or paste a replacement" : secret.placeholder}
              className={cn(FIELD, "font-mono")}
              data-testid="obs-receiver-secret-input"
              autoComplete="new-password"
              spellCheck={false}
            />
            <p className="mt-1 text-2xs text-muted-foreground">Stored encrypted. Reliant never shows it again, here or anywhere else.</p>
          </div>
        )}
        {problems.length > 0 && (
          <ul role="alert" className="space-y-0.5 rounded-md border border-destructive/40 bg-background px-3 py-2 text-xs text-destructive" data-testid="obs-receiver-problems">
            {problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        )}
        <div className="flex justify-end gap-2 border-t border-border/60 pt-4">
          <Button type="button" variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" size="sm" loading={pending} disabled={pending} data-testid="obs-receiver-save">
            {current ? "Save" : "Create receiver"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
