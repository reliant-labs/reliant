import { useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Check, Copy, Loader2, Plus, RefreshCw, Trash2 } from "lucide-react";
import { grpcClient } from "../../api/grpc-client";
import {
  RefreshLocalModelsRequestSchema,
  SetLocalModelEndpointsRequestSchema,
} from "../../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../../gen/reliant/v1/daemon_registry_pb";
import type {
  LocalModelEndpoint,
  LocalModelInfo,
  LocalModelInventory,
} from "../../gen/reliant/v1/tools_daemon_pb";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import Card, { CardInset } from "../forge-ui/card";
import { CustomEndpointsSection } from "./CustomEndpointsSection";
import Badge from "../forge-ui/badge";
import {
  COMMON_ENDPOINT_URLS,
  configuredUrls,
  contextSeverity,
  contextWarningText,
  describeEndpointError,
  endpointKindLabel,
  formatContextWindow,
  formatProbedAgo,
  isDaemonOnline,
  isManagedDaemon,
  machineLabel,
  newerInventory,
  validateEndpointUrl,
} from "./localModels";

const capabilityChips: Array<{ key: keyof LocalModelInfo; label: string }> = [
  { key: "supportsTools", label: "tools" },
  { key: "supportsVision", label: "vision" },
  { key: "supportsThinking", label: "thinking" },
];

function errorText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

function ModelRow({ model }: { model: LocalModelInfo }) {
  const ctx = formatContextWindow(model.contextWindow);
  const severity = contextSeverity(model.contextWindow);
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 py-1.5">
      <span className="min-w-0 truncate text-sm font-medium text-foreground">{model.name}</span>
      {model.parameterSize && (
        <span className="text-xs text-muted-foreground">{model.parameterSize}</span>
      )}
      {ctx && <span className="text-xs text-muted-foreground">{ctx}</span>}
      {severity === "warning" && (
        <Badge label={contextWarningText(model.contextWindow)} variant="warning" size="sm" />
      )}
      {severity === "soft" && (
        <span className="text-xs text-muted-foreground">{contextWarningText(model.contextWindow)}</span>
      )}
      <span className="flex gap-1">
        {capabilityChips
          .filter((c) => model[c.key])
          .map((c) => (
            <Badge key={c.label} label={c.label} size="sm" />
          ))}
      </span>
    </li>
  );
}

function CopyableCode({ code }: { code: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard unavailable; the text is still selectable.
    }
  };
  return (
    <div className="flex items-center gap-2 rounded-md border border-border/60 bg-card px-2.5 py-1.5">
      <code className="min-w-0 flex-1 overflow-x-auto font-mono text-xs text-foreground">{code}</code>
      <button
        type="button"
        aria-label="Copy command"
        onClick={copy}
        className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
      >
        {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
      </button>
    </div>
  );
}

function OllamaContextFix({ smallestContext }: { smallestContext: number }) {
  const size = formatContextWindow(BigInt(smallestContext)).replace(" ctx", "");
  return (
    <CardInset padding="sm" className="mt-2" data-testid="ollama-context-fix">
      <p className="text-xs text-foreground">
        Ollama is giving these models a {size} context. Raise it for agent work:
      </p>
      <ul className="mt-2 space-y-2 text-xs text-muted-foreground">
        <li>
          In the Ollama app: Settings → Context length.
        </li>
        <li className="space-y-1">
          <span>Or from the command line:</span>
          <CopyableCode code="OLLAMA_CONTEXT_LENGTH=64000 ollama serve" />
        </li>
      </ul>
      <p className="mt-2 text-xs text-muted-foreground">
        Then click Test connection — the daemon re-probes and reads the new context.
      </p>
    </CardInset>
  );
}

function EndpointBlock({
  endpoint,
  showEmbeddings,
  canRemove,
  onRemove,
}: {
  endpoint: LocalModelEndpoint;
  showEmbeddings: boolean;
  canRemove: boolean;
  onRemove: () => void;
}) {
  const configured = endpoint.source === "configured";
  const failure = describeEndpointError(endpoint);
  const chatModels = endpoint.models.filter((m) => m.supportsChat);
  const embeddingModels = endpoint.models.filter((m) => !m.supportsChat);
  const visible = showEmbeddings ? endpoint.models : chatModels;
  const smallContexts = chatModels
    .map((m) => Number(m.contextWindow))
    .filter((n) => contextSeverity(n) !== "none");
  const showOllamaFix = endpoint.kind === "ollama" && !failure && smallContexts.length > 0;
  return (
    <CardInset padding="sm" data-testid={`endpoint-${endpoint.id}`}>
      <div className="flex flex-wrap items-center gap-2">
        <Badge label={endpointKindLabel(endpoint.kind)} variant="info" size="sm" />
        <code className="min-w-0 truncate text-xs text-muted-foreground">{endpoint.baseUrl}</code>
        <Badge label={configured ? "configured" : "detected"} size="sm" />
        {configured && canRemove && (
          <button
            type="button"
            aria-label={`Remove ${endpoint.baseUrl}`}
            onClick={onRemove}
            className="ml-auto inline-flex items-center gap-1 rounded px-1.5 py-1 text-xs text-muted-foreground transition-colors hover:text-destructive"
          >
            <Trash2 className="h-3.5 w-3.5" />
            Remove
          </button>
        )}
      </div>
      {failure ? (
        <p role="alert" className="mt-2 text-xs text-destructive">
          {failure}
        </p>
      ) : visible.length === 0 ? (
        <p className="mt-2 text-xs text-muted-foreground">
          {embeddingModels.length > 0 && chatModels.length === 0
            ? "Only embedding models are available here — they can't chat."
            : "Connected, but no models are installed yet."}
        </p>
      ) : (
        <ul className="mt-1 divide-y divide-border/60">
          {visible.map((m) => (
            <ModelRow key={m.name} model={m} />
          ))}
        </ul>
      )}
      {showOllamaFix && <OllamaContextFix smallestContext={Math.min(...smallContexts)} />}
    </CardInset>
  );
}

function GettingStarted({ managed }: { managed: boolean }) {
  return (
    <div className="space-y-2 text-xs text-muted-foreground">
      {managed ? (
        <p>
          This is a cloud machine, so it has no GPU of its own to run local models on. To use a model
          server it can reach over the network, add its URL below.
        </p>
      ) : (
        <p>No local model server found on this machine. To get started, install Ollama and pull a model:</p>
      )}
      {!managed && (
        <CardInset padding="sm">
          <pre className="overflow-x-auto font-mono text-xs text-foreground">
            {"brew install ollama\nollama pull qwen3"}
          </pre>
        </CardInset>
      )}
      {!managed && <p>Reliant finds it automatically, or add its URL below.</p>}
    </div>
  );
}

function AddEndpointForm({
  existing,
  busy,
  onAdd,
}: {
  existing: string[];
  busy: boolean;
  onAdd: (url: string) => Promise<void>;
}) {
  const [value, setValue] = useState("");
  const [validation, setValidation] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const result = validateEndpointUrl(value, existing);
    if (!result.ok) {
      setValidation(result.message);
      return;
    }
    setValidation(null);
    await onAdd(result.url);
    setValue("");
  };

  return (
    <form onSubmit={submit} className="space-y-1.5">
      <div className="flex gap-2">
        <input
          type="text"
          aria-label="Endpoint URL"
          placeholder="http://localhost:11434/v1"
          value={value}
          onChange={(e) => {
            setValue(e.target.value);
            setValidation(null);
          }}
          className="min-w-0 flex-1 rounded-md border border-border bg-background px-2.5 py-1.5 text-xs text-foreground outline-none placeholder:text-muted-foreground/70 focus:border-primary"
        />
        <button
          type="submit"
          disabled={busy}
          className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-50"
        >
          <Plus className="h-3.5 w-3.5" />
          Add endpoint
        </button>
      </div>
      {validation && (
        <p role="alert" className="text-xs text-destructive">
          {validation}
        </p>
      )}
      <p className="text-xs text-muted-foreground">
        Common URLs: {COMMON_ENDPOINT_URLS.map((c) => `${c.label} ${c.url}`).join(" · ")}
      </p>
    </form>
  );
}

export function LocalModelsMachineCard({ daemon }: { daemon: DaemonInfo }) {
  const online = isDaemonOnline(daemon);
  const managed = isManagedDaemon(daemon);
  const [fresh, setFresh] = useState<LocalModelInventory | undefined>(undefined);
  const [busy, setBusy] = useState<"refresh" | "save" | null>(null);
  const [rpcError, setRpcError] = useState<string | null>(null);
  const [showEmbeddings, setShowEmbeddings] = useState(false);

  // The polled list can lag an RPC response; show whichever probe is newer.
  const inventory = newerInventory(daemon.localModels, fresh);
  const endpoints = inventory?.endpoints ?? [];
  const hasEmbeddings = endpoints.some((e) => e.models.some((m) => !m.supportsChat));

  const refresh = async () => {
    setBusy("refresh");
    setRpcError(null);
    try {
      const res = await grpcClient
        .daemonRegistry()
        .refreshLocalModels(create(RefreshLocalModelsRequestSchema, { daemonId: daemon.daemonId }));
      setFresh(res.localModels);
    } catch (e) {
      setRpcError(`Couldn't refresh: ${errorText(e)}`);
    } finally {
      setBusy(null);
    }
  };

  const saveUrls = async (baseUrls: string[]) => {
    setBusy("save");
    setRpcError(null);
    try {
      const res = await grpcClient
        .daemonRegistry()
        .setLocalModelEndpoints(
          create(SetLocalModelEndpointsRequestSchema, { daemonId: daemon.daemonId, baseUrls }),
        );
      setFresh(res.localModels);
    } catch (e) {
      setRpcError(`Couldn't save endpoints: ${errorText(e)}`);
    } finally {
      setBusy(null);
    }
  };

  const current = configuredUrls(inventory);

  return (
    <Card padding="md" data-testid={`local-models-${daemon.daemonId}`}>
      <div className="flex flex-wrap items-center gap-3">
        <h3 className="text-sm font-semibold text-foreground">{machineLabel(daemon)}</h3>
        <Badge
          label={online ? "Online" : "Offline"}
          variant={online ? "success" : "neutral"}
          size="sm"
          dot
        />
        {managed && <Badge label="Cloud" size="sm" />}
        {online && (
          <div className="ml-auto flex items-center gap-2">
            {inventory?.probedAt && (
              <span className="text-xs text-muted-foreground">
                Probed {formatProbedAgo(inventory.probedAt)}
              </span>
            )}
            <button
              type="button"
              onClick={refresh}
              disabled={busy !== null}
              className="inline-flex items-center gap-1.5 rounded-md border border-border px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:opacity-50"
            >
              {busy === "refresh" ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" data-testid="refresh-spinner" />
              ) : (
                <RefreshCw className="h-3.5 w-3.5" />
              )}
              Test connection
            </button>
          </div>
        )}
      </div>

      {!online ? (
        <p className="mt-3 text-xs text-muted-foreground">
          Machine is offline — local models are unavailable until it reconnects.
        </p>
      ) : (
        <div className="mt-3 space-y-3">
          {endpoints.length === 0 ? (
            <GettingStarted managed={managed} />
          ) : (
            <>
              {endpoints.map((endpoint) => (
                <EndpointBlock
                  key={endpoint.id}
                  endpoint={endpoint}
                  showEmbeddings={showEmbeddings}
                  canRemove={busy === null}
                  onRemove={() => saveUrls(current.filter((u) => u !== endpoint.baseUrl))}
                />
              ))}
              {hasEmbeddings && (
                <label className="flex items-center gap-2 text-xs text-muted-foreground">
                  <input
                    type="checkbox"
                    checked={showEmbeddings}
                    onChange={(e) => setShowEmbeddings(e.target.checked)}
                  />
                  Show embedding models
                </label>
              )}
            </>
          )}
          {rpcError && (
            <p role="alert" className="text-xs text-destructive">
              {rpcError}
            </p>
          )}
          <AddEndpointForm
            existing={current}
            busy={busy !== null}
            onAdd={(url) => saveUrls([...current, url])}
          />
        </div>
      )}
    </Card>
  );
}

export function LocalModelsSection() {
  const { daemons, loading } = useDaemonStatus();

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-base font-semibold text-foreground">Custom &amp; local models</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Run models like Ollama or LM Studio on your own hardware. Each machine's Reliant daemon finds
          the servers running on it and relays chats to them, so nothing about them is sent through
          Reliant's cloud. Servers it can't find, like a GPU cluster, can be added as custom endpoints.
        </p>
      </div>
      {daemons.length === 0 ? (
        <Card padding="md">
          <p className="text-sm text-muted-foreground">
            {loading
              ? "Loading machines…"
              : "Local models run on your machine through the Reliant daemon. Connect a machine in Settings → Machines, then come back here to see the model servers it can reach."}
          </p>
        </Card>
      ) : (
        daemons.map((d) => <LocalModelsMachineCard key={d.daemonId} daemon={d} />)
      )}
      <CustomEndpointsSection />
    </div>
  );
}
