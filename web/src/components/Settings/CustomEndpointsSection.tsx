import { useCallback, useEffect, useMemo, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { createClient } from "@connectrpc/connect";
import { Loader2, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { getTransport } from "../../api/grpc-client";
import {
  CreateModelEndpointRequestSchema,
  DeleteModelEndpointRequestSchema,
  ListModelEndpointsRequestSchema,
  ModelEndpointRoute,
  ModelEndpointService,
  TestModelEndpointRequestSchema,
  UpdateModelEndpointRequestSchema,
} from "../../gen/reliant/v1/model_endpoint_pb";
import type { ModelEndpoint } from "../../gen/reliant/v1/model_endpoint_pb";
import type { LocalModelEndpoint } from "../../gen/reliant/v1/tools_daemon_pb";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import Card, { CardInset } from "../forge-ui/card";
import Badge from "../forge-ui/badge";
import { describeEndpointError, endpointKindLabel, formatContextWindow, isDaemonOnline, machineLabel } from "./localModels";
import {
  CREDENTIAL_STORE_NOTE,
  draftToInput,
  emptyDraft,
  endpointToDraft,
  formatLatency,
  hasModelErrors,
  mergeProbeIntoDraft,
  validateDraft,
  validateModelDraft,
} from "./modelEndpoints";
import type { EndpointDraft, ModelDraft, Tri } from "./modelEndpoints";

const client = () => createClient(ModelEndpointService, getTransport());

const inputClass =
  "w-full min-w-0 rounded-md border border-border bg-background px-2.5 py-1.5 text-xs text-foreground outline-none placeholder:text-muted-foreground/70 focus:border-primary disabled:cursor-not-allowed disabled:opacity-60";
const buttonPrimary =
  "inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-50";
const buttonGhost =
  "inline-flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-accent disabled:opacity-50";

function errorText(e: unknown): string {
  if (e && typeof e === "object" && "rawMessage" in e && typeof (e as { rawMessage: unknown }).rawMessage === "string") {
    return (e as { rawMessage: string }).rawMessage;
  }
  return e instanceof Error ? e.message : String(e);
}

function Field({ label, hint, error, children, htmlFor }: { label: string; hint?: string; error?: string | null; children: React.ReactNode; htmlFor?: string }) {
  return (
    <div className="space-y-1">
      <label htmlFor={htmlFor} className="text-xs font-medium text-foreground">
        {label}
      </label>
      {children}
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : hint ? (
        <p className="text-xs text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  );
}

function TriSelect({ label, value, onChange }: { label: string; value: Tri; onChange: (v: Tri) => void }) {
  return (
    <label className="flex items-center gap-1.5 text-xs text-muted-foreground">
      {label}
      <select
        aria-label={label}
        value={value}
        onChange={(e) => onChange(e.target.value as Tri)}
        className="rounded-md border border-border bg-background px-1.5 py-1 text-xs text-foreground focus:border-primary"
      >
        <option value="auto">Auto</option>
        <option value="on">On</option>
        <option value="off">Off</option>
      </select>
    </label>
  );
}

function ModelSettingsRow({ model, discovered, onChange }: { model: ModelDraft; discovered?: { ctx: number; tools: boolean; vision: boolean; thinking: boolean }; onChange: (m: ModelDraft) => void }) {
  const [open, setOpen] = useState(false);
  const errors = validateModelDraft(model);
  const patch = (p: Partial<ModelDraft>) => onChange({ ...model, ...p });
  const ctx = discovered?.ctx ? formatContextWindow(BigInt(discovered.ctx)) : "";
  return (
    <li className="py-2" data-testid={`model-${model.name}`}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className={`min-w-0 truncate text-sm font-medium ${model.hidden ? "text-muted-foreground line-through" : "text-foreground"}`}>{model.name}</span>
        {ctx && <span className="text-xs text-muted-foreground">{ctx}</span>}
        <label className="ml-auto flex items-center gap-1.5 text-xs text-muted-foreground">
          <input type="checkbox" aria-label={`Hide ${model.name}`} checked={model.hidden} onChange={(e) => patch({ hidden: e.target.checked })} />
          Hide from picker
        </label>
        <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={open} aria-label={`Settings for ${model.name}`} className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline">
          {open ? "Done" : "Settings"}
        </button>
      </div>
      {open && (
        <CardInset padding="sm" className="mt-2 space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <Field label="Context window (tokens)" error={errors.contextWindow} hint={ctx ? `Server reports ${ctx}` : "Blank uses 8K unless the server reports one"} htmlFor={`ctx-${model.name}`}>
              <input id={`ctx-${model.name}`} className={inputClass} inputMode="numeric" placeholder="auto" value={model.contextWindow} onChange={(e) => patch({ contextWindow: e.target.value })} />
            </Field>
            <Field label="Max output (tokens)" error={errors.maxOutputTokens} htmlFor={`out-${model.name}`}>
              <input id={`out-${model.name}`} className={inputClass} inputMode="numeric" placeholder="auto" value={model.maxOutputTokens} onChange={(e) => patch({ maxOutputTokens: e.target.value })} />
            </Field>
            <Field label="Default temperature" error={errors.temperature} hint="Used unless the chat sets one" htmlFor={`temp-${model.name}`}>
              <input id={`temp-${model.name}`} className={inputClass} inputMode="decimal" placeholder="server default" value={model.temperature} onChange={(e) => patch({ temperature: e.target.value })} />
            </Field>
            <Field label="Default top-p" error={errors.topP} htmlFor={`topp-${model.name}`}>
              <input id={`topp-${model.name}`} className={inputClass} inputMode="decimal" placeholder="server default" value={model.topP} onChange={(e) => patch({ topP: e.target.value })} />
            </Field>
          </div>
          <div className="flex flex-wrap gap-4">
            <TriSelect label="Tools" value={model.tools} onChange={(v) => patch({ tools: v })} />
            <TriSelect label="Vision" value={model.vision} onChange={(v) => patch({ vision: v })} />
            <TriSelect label="Thinking" value={model.thinking} onChange={(v) => patch({ thinking: v })} />
          </div>
          <Field label="Advanced: extra request JSON" error={errors.extraBodyJson} hint='Merged into every request, e.g. {"min_p": 0.05}. model, messages, tools and stream are reserved.' htmlFor={`extra-${model.name}`}>
            <textarea id={`extra-${model.name}`} rows={3} spellCheck={false} className={`${inputClass} font-mono`} placeholder="{}" value={model.extraBodyJson} onChange={(e) => patch({ extraBodyJson: e.target.value })} />
          </Field>
        </CardInset>
      )}
    </li>
  );
}

function ProbeSummary({ probe, latencyMs }: { probe: LocalModelEndpoint | undefined; latencyMs?: bigint }) {
  if (!probe) return null;
  const failure = describeEndpointError({ error: probe.error, kind: probe.kind, baseUrl: probe.baseUrl });
  if (failure) {
    return (
      <p role="alert" className="text-xs text-destructive">
        {failure}
      </p>
    );
  }
  const chat = probe.models.filter((m) => m.supportsChat).length;
  return (
    <p className="text-xs text-foreground" data-testid="probe-ok">
      Connected{probe.kind && probe.kind !== "openai_compatible" ? ` to ${endpointKindLabel(probe.kind)}` : ""}: {chat} model{chat === 1 ? "" : "s"}
      {latencyMs !== undefined ? ` in ${formatLatency(latencyMs)}` : ""}.
    </p>
  );
}

function EndpointForm({
  initial,
  editingId,
  credentialsAvailable,
  onSaved,
  onCancel,
}: {
  initial: EndpointDraft;
  editingId?: string;
  credentialsAvailable: boolean;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const { daemons } = useDaemonStatus();
  const [draft, setDraft] = useState<EndpointDraft>(initial);
  const [fieldError, setFieldError] = useState<{ field: string; message: string } | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [probe, setProbe] = useState<{ probe: LocalModelEndpoint | undefined; latencyMs?: bigint } | null>(null);
  const [discovered, setDiscovered] = useState<Record<string, { ctx: number; tools: boolean; vision: boolean; thinking: boolean }>>({});

  const patch = (p: Partial<EndpointDraft>) => {
    setDraft((d) => ({ ...d, ...p }));
    setFieldError(null);
    setSaveError(null);
  };

  const absorbProbe = (p: LocalModelEndpoint | undefined) => {
    if (!p) return;
    const chat = p.models.filter((m) => m.supportsChat);
    setDiscovered(Object.fromEntries(chat.map((m) => [m.name, { ctx: Number(m.contextWindow), tools: m.supportsTools, vision: m.supportsVision, thinking: m.supportsThinking }])));
    setDraft((d) => ({ ...d, models: mergeProbeIntoDraft(d.models, chat.map((m) => m.name)) }));
  };

  useEffect(() => {
    if (editingId) {
      // Show what the server last reported without making the user re-test.
      void (async () => {
        try {
          const list = await client().listModelEndpoints(create(ListModelEndpointsRequestSchema));
          const own = list.endpoints.find((e) => e.id === editingId);
          if (own?.probe) {
            setProbe({ probe: own.probe });
            absorbProbe(own.probe);
          }
        } catch {
          /* the form still works without the cached probe */
        }
      })();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editingId]);

  const test = async () => {
    const v = validateDraft(draft);
    if (!v.ok && v.field !== "models") {
      setFieldError({ field: v.field, message: v.message });
      return;
    }
    setTesting(true);
    setSaveError(null);
    try {
      const target = editingId
        ? ({ case: "id", value: editingId } as const)
        : ({ case: "draft", value: draftToInput(draft, { includeKey: credentialsAvailable }) } as const);
      const resp = await client().testModelEndpoint(create(TestModelEndpointRequestSchema, { target }));
      setProbe({ probe: resp.probe, latencyMs: resp.latencyMs });
      absorbProbe(resp.probe);
    } catch (e) {
      setProbe(null);
      setSaveError(errorText(e));
    } finally {
      setTesting(false);
    }
  };

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    const v = validateDraft(draft);
    if (!v.ok) {
      setFieldError({ field: v.field, message: v.message });
      return;
    }
    setSaving(true);
    setSaveError(null);
    try {
      const input = draftToInput(draft, { includeKey: credentialsAvailable });
      if (editingId) {
        await client().updateModelEndpoint(create(UpdateModelEndpointRequestSchema, { id: editingId, endpoint: input }));
      } else {
        await client().createModelEndpoint(create(CreateModelEndpointRequestSchema, { endpoint: input }));
      }
      onSaved();
    } catch (err) {
      setSaveError(errorText(err));
    } finally {
      setSaving(false);
    }
  };

  const onlineDaemons = daemons.filter((d) => isDaemonOnline(d));
  const errFor = (f: string) => (fieldError?.field === f ? fieldError.message : null);

  return (
    <form onSubmit={save} className="space-y-4" aria-label={editingId ? "Edit custom endpoint" : "New custom endpoint"}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Name" error={errFor("name")} htmlFor="ep-name" hint="Shown as the group in the model picker.">
          <input id="ep-name" className={inputClass} placeholder="Lab GPU cluster" value={draft.name} onChange={(e) => patch({ name: e.target.value })} />
        </Field>
        <Field label="Base URL" error={errFor("baseUrl")} htmlFor="ep-url" hint="OpenAI-compatible, usually ending in /v1.">
          <input id="ep-url" className={inputClass} placeholder="https://llm.example.com/v1" value={draft.baseUrl} onChange={(e) => patch({ baseUrl: e.target.value })} />
        </Field>
      </div>

      <fieldset className="space-y-2">
        <legend className="text-xs font-medium text-foreground">How should Reliant reach it?</legend>
        <div className="grid gap-2 sm:grid-cols-2">
          <label className={`cursor-pointer rounded-md border p-3 text-xs transition-colors ${draft.route === "direct" ? "border-primary bg-card" : "border-border/60 bg-background hover:border-border"}`}>
            <span className="flex items-center gap-2 font-medium text-foreground">
              <input type="radio" name="route" aria-label="Reliant cloud" checked={draft.route === "direct"} onChange={() => patch({ route: "direct", daemonId: "" })} />
              Reliant cloud
            </span>
            <span className="mt-1 block text-muted-foreground">For servers on the public internet. Requests go straight from Reliant's servers to the URL.</span>
          </label>
          <label className={`cursor-pointer rounded-md border p-3 text-xs transition-colors ${draft.route === "via_daemon" ? "border-primary bg-card" : "border-border/60 bg-background hover:border-border"}`}>
            <span className="flex items-center gap-2 font-medium text-foreground">
              <input type="radio" name="route" aria-label="Through one of my machines" checked={draft.route === "via_daemon"} onChange={() => patch({ route: "via_daemon" })} />
              Through one of my machines
            </span>
            <span className="mt-1 block text-muted-foreground">For localhost, a LAN, or a server behind a VPN. Pick a machine that can reach it; it relays the requests, so the URL is resolved from that machine.</span>
          </label>
        </div>
        {draft.route === "via_daemon" && (
          <Field label="Machine" error={errFor("daemonId")} htmlFor="ep-machine">
            <select id="ep-machine" className={inputClass} value={draft.daemonId} onChange={(e) => patch({ daemonId: e.target.value })}>
              <option value="">Choose a machine…</option>
              {daemons.map((d) => (
                <option key={d.daemonId} value={d.daemonId} disabled={!isDaemonOnline(d) && d.daemonId !== draft.daemonId}>
                  {machineLabel(d)}
                  {isDaemonOnline(d) ? "" : " (offline)"}
                </option>
              ))}
            </select>
            {daemons.length > 0 && onlineDaemons.length === 0 && (
              <p className="mt-1 text-xs text-muted-foreground">All your machines are offline. You can still save; use Test connection once one is back.</p>
            )}
          </Field>
        )}
      </fieldset>

      <div className="space-y-1">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="API key" htmlFor="ep-key">
            <input id="ep-key" type="password" autoComplete="off" className={inputClass} placeholder="Optional" disabled={!credentialsAvailable} value={draft.apiKey} onChange={(e) => patch({ apiKey: e.target.value })} />
          </Field>
          <Field label="Custom headers" htmlFor="ep-headers">
            <input id="ep-headers" className={inputClass} placeholder="Optional" disabled />
          </Field>
        </div>
        {!credentialsAvailable && (
          <p className="text-xs text-muted-foreground" data-testid="credential-note">
            {CREDENTIAL_STORE_NOTE}
          </p>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <button type="button" onClick={test} disabled={testing || saving} className={buttonGhost}>
          {testing ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
          Test connection
        </button>
        <ProbeSummary probe={probe?.probe} latencyMs={probe?.latencyMs} />
      </div>

      {draft.models.length > 0 && (
        <div>
          <h4 className="text-xs font-medium text-foreground">Models</h4>
          <ul className="mt-1 divide-y divide-border/60">
            {draft.models.map((m) => (
              <ModelSettingsRow
                key={m.name}
                model={m}
                discovered={discovered[m.name]}
                onChange={(next) => setDraft((d) => ({ ...d, models: d.models.map((x) => (x.name === m.name ? next : x)) }))}
              />
            ))}
          </ul>
          {fieldError?.field === "models" && (
            <p role="alert" className="mt-1 text-xs text-destructive">
              {fieldError.message}
            </p>
          )}
        </div>
      )}

      {saveError && (
        <p role="alert" className="text-xs text-destructive" data-testid="save-error">
          {saveError}
        </p>
      )}

      <div className="flex gap-2">
        <button type="submit" disabled={saving} className={buttonPrimary}>
          {saving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          {editingId ? "Save changes" : "Save endpoint"}
        </button>
        <button type="button" onClick={onCancel} className={buttonGhost}>
          Cancel
        </button>
      </div>
    </form>
  );
}

function EndpointRow({ endpoint, machine, onEdit, onDelete, deleting }: { endpoint: ModelEndpoint; machine?: string; onEdit: () => void; onDelete: () => void; deleting: boolean }) {
  const failure = endpoint.probe ? describeEndpointError({ error: endpoint.probe.error, kind: endpoint.probe.kind, baseUrl: endpoint.probe.baseUrl }) : "";
  const visible = endpoint.models.filter((m) => !m.hidden);
  const via = endpoint.route === ModelEndpointRoute.VIA_DAEMON;
  return (
    <CardInset padding="sm" data-testid={`custom-endpoint-${endpoint.id}`}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-semibold text-foreground">{endpoint.name}</span>
        <Badge label={via ? `via ${machine ?? "machine"}` : "Reliant cloud"} size="sm" variant={via ? "info" : "neutral"} />
        {endpoint.probe?.kind && endpoint.probe.kind !== "openai_compatible" && <Badge label={endpointKindLabel(endpoint.probe.kind)} size="sm" />}
        <code className="min-w-0 truncate text-xs text-muted-foreground">{endpoint.baseUrl}</code>
        <span className="ml-auto flex gap-1">
          <button type="button" aria-label={`Edit ${endpoint.name}`} onClick={onEdit} className="inline-flex items-center gap-1 rounded px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:text-foreground">
            <Pencil className="h-3.5 w-3.5" />
            Edit
          </button>
          <button type="button" aria-label={`Delete ${endpoint.name}`} onClick={onDelete} disabled={deleting} className="inline-flex items-center gap-1 rounded px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:text-destructive disabled:opacity-50">
            {deleting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
            Delete
          </button>
        </span>
      </div>
      {failure ? (
        <p role="alert" className="mt-2 text-xs text-destructive">
          {failure}
        </p>
      ) : visible.length === 0 ? (
        <p className="mt-2 text-xs text-muted-foreground">{endpoint.probe ? "No models to offer yet." : "Not tested yet."}</p>
      ) : (
        <p className="mt-2 text-xs text-muted-foreground">
          {visible.length} model{visible.length === 1 ? "" : "s"}: {visible.slice(0, 4).map((m) => m.name).join(", ")}
          {visible.length > 4 ? `, +${visible.length - 4} more` : ""}
        </p>
      )}
    </CardInset>
  );
}

/**
 * User-configured OpenAI-compatible endpoints: a GPU cluster, vLLM, a hosted
 * inference API. Auto-detected servers live in the per-machine cards above.
 */
export function CustomEndpointsSection({ credentialsAvailable = false }: { credentialsAvailable?: boolean }) {
  const { daemons } = useDaemonStatus();
  const [endpoints, setEndpoints] = useState<ModelEndpoint[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [mode, setMode] = useState<{ kind: "list" } | { kind: "add" } | { kind: "edit"; endpoint: ModelEndpoint }>({ kind: "list" });
  const [deleting, setDeleting] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<ModelEndpoint | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  const machines = useMemo(() => new Map(daemons.map((d) => [d.daemonId, machineLabel(d)])), [daemons]);

  const load = useCallback(async () => {
    try {
      const resp = await client().listModelEndpoints(create(ListModelEndpointsRequestSchema));
      setEndpoints(resp.endpoints);
      setLoadError(null);
    } catch (e) {
      setLoadError(errorText(e));
      setEndpoints((prev) => prev ?? []);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const remove = async (ep: ModelEndpoint) => {
    setDeleting(ep.id);
    setDeleteError(null);
    try {
      await client().deleteModelEndpoint(create(DeleteModelEndpointRequestSchema, { id: ep.id }));
      setConfirmDelete(null);
      await load();
    } catch (e) {
      setDeleteError(errorText(e));
    } finally {
      setDeleting(null);
    }
  };

  return (
    <Card padding="md" data-testid="custom-endpoints">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <h3 className="text-sm font-semibold text-foreground">Custom endpoints</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            Your own OpenAI-compatible servers: a GPU cluster, vLLM, LM Studio, or a hosted inference API. Their models appear in the model picker under the endpoint's name.
          </p>
        </div>
        {mode.kind === "list" && (
          <button type="button" onClick={() => setMode({ kind: "add" })} className={buttonPrimary}>
            <Plus className="h-3.5 w-3.5" />
            Add custom endpoint
          </button>
        )}
      </div>

      <div className="mt-3 space-y-2">
        {mode.kind === "add" && (
          <CardInset padding="md">
            <EndpointForm
              initial={emptyDraft()}
              credentialsAvailable={credentialsAvailable}
              onSaved={() => {
                setMode({ kind: "list" });
                void load();
              }}
              onCancel={() => setMode({ kind: "list" })}
            />
          </CardInset>
        )}
        {mode.kind === "edit" && (
          <CardInset padding="md">
            <EndpointForm
              key={mode.endpoint.id}
              initial={endpointToDraft(mode.endpoint)}
              editingId={mode.endpoint.id}
              credentialsAvailable={credentialsAvailable}
              onSaved={() => {
                setMode({ kind: "list" });
                void load();
              }}
              onCancel={() => setMode({ kind: "list" })}
            />
          </CardInset>
        )}

        {mode.kind === "list" && endpoints === null && <p className="text-xs text-muted-foreground">Loading endpoints…</p>}
        {loadError && (
          <p role="alert" className="text-xs text-destructive">
            Couldn't load your endpoints: {loadError}
          </p>
        )}
        {mode.kind === "list" && endpoints !== null && endpoints.length === 0 && !loadError && (
          <p className="text-xs text-muted-foreground" data-testid="no-endpoints">
            No custom endpoints yet.
          </p>
        )}
        {mode.kind === "list" &&
          endpoints?.map((ep) => (
            <EndpointRow
              key={ep.id}
              endpoint={ep}
              machine={machines.get(ep.daemonId)}
              deleting={deleting === ep.id}
              onEdit={() => setMode({ kind: "edit", endpoint: ep })}
              onDelete={() => setConfirmDelete(ep)}
            />
          ))}

        {confirmDelete && (
          <CardInset padding="sm" role="alertdialog" aria-label={`Delete ${confirmDelete.name}`} data-testid="confirm-delete">
            <p className="text-xs text-foreground">
              Delete <strong>{confirmDelete.name}</strong>? Its models disappear from the picker, and any chat set to use one will need a new model.
            </p>
            {deleteError && (
              <p role="alert" className="mt-1 text-xs text-destructive">
                {deleteError}
              </p>
            )}
            <div className="mt-2 flex gap-2">
              <button type="button" disabled={deleting !== null} onClick={() => remove(confirmDelete)} className="inline-flex items-center gap-1.5 rounded-md bg-destructive px-3 py-1.5 text-xs font-medium text-destructive-foreground transition-colors hover:bg-destructive/90 disabled:opacity-50">
                Delete endpoint
              </button>
              <button type="button" onClick={() => { setConfirmDelete(null); setDeleteError(null); }} className={buttonGhost}>
                Keep it
              </button>
            </div>
          </CardInset>
        )}
      </div>
    </Card>
  );
}
