import { useState, useEffect, useCallback, useMemo } from "react";
import { create } from "@bufbuild/protobuf";
import {
  Copy,
  Check,
  Plus,
  Trash2,
  Loader2,
  ShieldAlert,
  Terminal,
} from "lucide-react";
import { grpcClient } from "../../api/grpc-client";
import {
  ListConnectorsRequestSchema,
  CreateConnectorRequestSchema,
  RevokeConnectorRequestSchema,
  ListConnectorActivityRequestSchema,
  ListAvailableToolsRequestSchema,
  ConnectorExecMode,
} from "../../gen/reliant/v1/connector_pb";
import type {
  Connector,
  ConnectorTool,
  ConnectorActivity,
} from "../../gen/reliant/v1/connector_pb";
import { ListDaemonsRequestSchema } from "../../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../../gen/reliant/v1/daemon_registry_pb";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Badge } from "../ui/Badge";
import { cn } from "../../lib/utils";

/**
 * Connector settings: grants that let a third-party MCP client (ChatGPT,
 * Claude, and their mobile apps) run tools inside one of the user's cloud
 * workspaces.
 *
 * The create form is a consent screen, so it is built to make the scope of a
 * grant legible rather than to be quick to fill in: read-only by default,
 * shell access off unless deliberately enabled, and one workspace per
 * connector.
 */
export function ConnectorSettings() {
  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [tools, setTools] = useState<ConnectorTool[]>([]);
  const [daemons, setDaemons] = useState<DaemonInfo[]>([]);
  const [activity, setActivity] = useState<ConnectorActivity[]>([]);

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  // Newly minted credential. Held in component state only — it is returned
  // exactly once and is unrecoverable after this view is dismissed.
  const [created, setCreated] = useState<{
    credential: string;
    mcpUrl: string;
    name: string;
  } | null>(null);

  const [form, setForm] = useState({
    name: "",
    daemonId: "",
    pathRoot: "/workspace",
    selectedTools: new Set<string>(),
    execMode: ConnectorExecMode.DENY,
    execAllowlist: "git, go, npm",
  });

  const loadAll = useCallback(async () => {
    try {
      setError(null);
      const client = grpcClient.connector();

      const [connectorsRes, toolsRes, activityRes] = await Promise.all([
        client.listConnectors(create(ListConnectorsRequestSchema, {})),
        client.listAvailableTools(create(ListAvailableToolsRequestSchema, {})),
        client.listConnectorActivity(
          create(ListConnectorActivityRequestSchema, { limit: 50 })
        ),
      ]);

      setConnectors(connectorsRes.connectors);
      setTools(toolsRes.tools);
      setActivity(activityRes.activity);

      // Workspaces are listed separately: a connector must be bound to one,
      // and this call can fail independently (no control plane) without
      // making the rest of the page useless.
      try {
        const daemonsRes = await grpcClient
          .daemonRegistry()
          .listDaemons(create(ListDaemonsRequestSchema, {}));
        setDaemons(daemonsRes.daemons);
      } catch {
        setDaemons([]);
      }
    } catch (err) {
      console.error("Failed to load connectors:", err);
      setError("Failed to load connectors.");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadAll();
  }, [loadAll]);

  // Default the tool selection to everything read-only. A grant should start
  // at the least privilege that is still useful, and widen deliberately.
  useEffect(() => {
    if (tools.length > 0 && form.selectedTools.size === 0) {
      setForm((f) => ({
        ...f,
        selectedTools: new Set(
          tools.filter((t) => !t.mutating).map((t) => t.name)
        ),
      }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tools]);

  const selectedNeedsExec = useMemo(
    () =>
      tools.some((t) => form.selectedTools.has(t.name) && t.needsExec),
    [tools, form.selectedTools]
  );

  // The server rejects a shell tool granted without an exec mode, because such
  // a connector would refuse every call to it. Surface that here rather than
  // letting the user discover it on submit.
  const execModeRequired =
    selectedNeedsExec && form.execMode === ConnectorExecMode.DENY;

  const toggleTool = (name: string) => {
    setForm((f) => {
      const next = new Set(f.selectedTools);
      if (next.has(name)) {
        next.delete(name);
      } else {
        next.add(name);
      }
      return { ...f, selectedTools: next };
    });
  };

  const handleCreate = async () => {
    if (!form.name.trim() || !form.daemonId || form.selectedTools.size === 0) {
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const res = await grpcClient.connector().createConnector(
        create(CreateConnectorRequestSchema, {
          name: form.name.trim(),
          daemonId: form.daemonId,
          allowedTools: Array.from(form.selectedTools),
          pathRoot: form.pathRoot.trim(),
          execMode: form.execMode,
          execAllowlist:
            form.execMode === ConnectorExecMode.ALLOWLIST
              ? form.execAllowlist
                  .split(",")
                  .map((s) => s.trim())
                  .filter(Boolean)
              : [],
        })
      );

      setCreated({
        credential: res.credential,
        mcpUrl: res.mcpUrl,
        name: form.name.trim(),
      });
      setShowCreate(false);
      setForm((f) => ({ ...f, name: "" }));
      await loadAll();
    } catch (err) {
      console.error("Failed to create connector:", err);
      setError(
        err instanceof Error ? err.message : "Failed to create connector."
      );
    } finally {
      setSubmitting(false);
    }
  };

  const handleRevoke = async (id: string) => {
    setError(null);
    try {
      await grpcClient
        .connector()
        .revokeConnector(create(RevokeConnectorRequestSchema, { id }));
      setRevokingId(null);
      await loadAll();
    } catch (err) {
      console.error("Failed to revoke connector:", err);
      setError("Failed to revoke connector.");
    }
  };

  const handleCopy = async (text: string, key: string) => {
    await navigator.clipboard.writeText(text);
    setCopied(key);
    setTimeout(() => setCopied(null), 2000);
  };

  const formatDate = (iso?: string) => {
    if (!iso) return "Never";
    return new Date(iso).toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    });
  };

  const deniedCount = activity.filter((a) => a.denied).length;

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="w-5 h-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-semibold mb-2">Connectors</h2>
        <p className="text-sm text-muted-foreground">
          Let ChatGPT, Claude, or another MCP client run tools inside one of
          your cloud workspaces — including from your phone, where those apps
          cannot run tools locally.
        </p>
      </div>

      {error && (
        <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-3">
          <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
        </div>
      )}

      {/* Newly created credential — shown once, never retrievable again. */}
      {created && (
        <div className="border border-green-200 dark:border-green-800 rounded-lg p-6 space-y-4 bg-green-50 dark:bg-green-950/20">
          <h3 className="font-medium text-green-800 dark:text-green-200">
            Connector “{created.name}” created
          </h3>

          <div className="space-y-2">
            <label className="text-xs text-muted-foreground">Server URL</label>
            <div className="flex items-center gap-2">
              <Input value={created.mcpUrl} readOnly className="font-mono text-sm" />
              <Button
                variant="outline"
                size="sm"
                onClick={() => handleCopy(created.mcpUrl, "url")}
              >
                {copied === "url" ? (
                  <Check className="w-4 h-4" />
                ) : (
                  <Copy className="w-4 h-4" />
                )}
              </Button>
            </div>
          </div>

          <div className="space-y-2">
            <label className="text-xs text-muted-foreground">
              Credential (Bearer token)
            </label>
            <div className="flex items-center gap-2">
              <Input
                value={created.credential}
                readOnly
                className="font-mono text-sm"
              />
              <Button
                variant="outline"
                size="sm"
                onClick={() => handleCopy(created.credential, "cred")}
              >
                {copied === "cred" ? (
                  <Check className="w-4 h-4" />
                ) : (
                  <Copy className="w-4 h-4" />
                )}
              </Button>
            </div>
          </div>

          <p className="text-sm text-yellow-700 dark:text-yellow-400 font-medium">
            This credential is shown only once. Copy it now — it cannot be
            retrieved later.
          </p>

          <Button variant="ghost" size="sm" onClick={() => setCreated(null)}>
            Done
          </Button>
        </div>
      )}

      {/* Existing connectors */}
      <div className="border border-border/40 rounded-lg p-6 space-y-4">
        <div className="flex items-center justify-between">
          <h3 className="font-medium">Your connectors</h3>
          {!showCreate && (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setShowCreate(true)}
              leftIcon={<Plus className="w-4 h-4" />}
              disabled={daemons.length === 0}
            >
              New connector
            </Button>
          )}
        </div>

        {daemons.length === 0 && (
          <p className="text-sm text-muted-foreground">
            You need a cloud workspace before you can create a connector.
          </p>
        )}

        {connectors.length === 0 && daemons.length > 0 && (
          <p className="text-sm text-muted-foreground">
            No connectors yet.
          </p>
        )}

        <div className="space-y-2">
          {connectors.map((c) => {
            const revoked = Boolean(c.revokedAt);
            return (
              <div
                key={c.id}
                className={cn(
                  "flex items-start justify-between gap-4 border border-border/40 rounded-lg p-4",
                  revoked && "opacity-60"
                )}
              >
                <div className="min-w-0 space-y-1">
                  <div className="flex items-center gap-2">
                    <span className="font-medium truncate">{c.name}</span>
                    {revoked ? (
                      <Badge variant="destructive">Revoked</Badge>
                    ) : (
                      <Badge variant="success">Active</Badge>
                    )}
                    {c.execMode !== ConnectorExecMode.DENY && (
                      <Badge variant="warning">
                        <Terminal className="w-3 h-3 mr-1 inline" />
                        Shell
                      </Badge>
                    )}
                  </div>
                  <p className="text-xs text-muted-foreground font-mono truncate">
                    {c.tokenPrefix}… · {c.pathRoot}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    {c.allowedTools.length} tools · last used{" "}
                    {formatDate(c.lastUsedAt)}
                  </p>
                </div>

                {!revoked && (
                  <div className="shrink-0">
                    {revokingId === c.id ? (
                      <div className="flex items-center gap-2">
                        <Button
                          variant="destructive"
                          size="sm"
                          onClick={() => handleRevoke(c.id)}
                        >
                          Confirm
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => setRevokingId(null)}
                        >
                          Cancel
                        </Button>
                      </div>
                    ) : (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setRevokingId(c.id)}
                        leftIcon={<Trash2 className="w-4 h-4" />}
                      >
                        Revoke
                      </Button>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>

      {/* Create form — the consent screen. */}
      {showCreate && (
        <div className="border border-border/40 rounded-lg p-6 space-y-5">
          <h3 className="font-medium">New connector</h3>

          <div className="space-y-2">
            <label className="text-sm font-medium">Name</label>
            <Input
              placeholder="ChatGPT on my phone"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              autoFocus
            />
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium">Workspace</label>
            <select
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
              value={form.daemonId}
              onChange={(e) => setForm({ ...form, daemonId: e.target.value })}
            >
              <option value="">Select a workspace…</option>
              {daemons.map((d) => (
                <option key={d.daemonId} value={d.daemonId}>
                  {d.hostname || d.daemonId}
                </option>
              ))}
            </select>
            <p className="text-xs text-muted-foreground">
              A connector reaches exactly one workspace. If it is ever misused,
              nothing outside this workspace is exposed.
            </p>
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium">Allowed directory</label>
            <Input
              value={form.pathRoot}
              onChange={(e) => setForm({ ...form, pathRoot: e.target.value })}
              className="font-mono text-sm"
            />
            <p className="text-xs text-muted-foreground">
              File access is confined to this directory, including through
              symlinks.
            </p>
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium">Tools</label>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
              {tools.map((t) => (
                <label
                  key={t.name}
                  className="flex items-start gap-2 border border-border/40 rounded-md p-2 cursor-pointer hover:bg-muted/40"
                >
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={form.selectedTools.has(t.name)}
                    onChange={() => toggleTool(t.name)}
                  />
                  <span className="min-w-0">
                    <span className="flex items-center gap-1.5">
                      <span className="text-sm font-mono">{t.name}</span>
                      {t.mutating && (
                        <Badge variant="warning">writes</Badge>
                      )}
                    </span>
                    <span className="block text-xs text-muted-foreground line-clamp-2">
                      {t.description}
                    </span>
                  </span>
                </label>
              ))}
            </div>
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium">Shell access</label>
            <select
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
              value={form.execMode}
              onChange={(e) =>
                setForm({ ...form, execMode: Number(e.target.value) })
              }
            >
              <option value={ConnectorExecMode.DENY}>
                No commands
              </option>
              <option value={ConnectorExecMode.ALLOWLIST}>
                Only specific programs
              </option>
              <option value={ConnectorExecMode.UNRESTRICTED}>
                Any command, through a shell
              </option>
            </select>

            {form.execMode === ConnectorExecMode.ALLOWLIST && (
              <>
                <Input
                  value={form.execAllowlist}
                  onChange={(e) =>
                    setForm({ ...form, execAllowlist: e.target.value })
                  }
                  placeholder="git, go, npm"
                  className="font-mono text-sm"
                />
                <p className="text-xs text-muted-foreground">
                  Only these programs can run, and they run without a shell —
                  so pipes, redirection, and chaining are unavailable. This is
                  the setting to use for a workspace you care about.
                </p>
              </>
            )}

            {execModeRequired && (
              <p className="text-xs text-yellow-700 dark:text-yellow-400">
                You selected a tool that runs shell commands, so shell access
                cannot be “No shell commands”.
              </p>
            )}

            {form.execMode === ConnectorExecMode.UNRESTRICTED && (
              <div className="flex gap-2 rounded-md border border-yellow-200 dark:border-yellow-800 bg-yellow-50 dark:bg-yellow-950/20 p-3">
                <ShieldAlert className="w-4 h-4 shrink-0 text-yellow-700 dark:text-yellow-400 mt-0.5" />
                <p className="text-xs text-yellow-800 dark:text-yellow-300">
                  Commands run through a shell, so the assistant can run
                  anything in this workspace — and it acts on text it reads,
                  including text from web pages and repositories. Only use this
                  for a workspace you are willing to have modified or
                  destroyed. Prefer “Only specific programs”, which runs
                  without a shell.
                </p>
              </div>
            )}
          </div>

          <div className="flex items-center gap-2 pt-2">
            <Button
              variant="primary"
              size="sm"
              onClick={handleCreate}
              disabled={
                submitting ||
                !form.name.trim() ||
                !form.daemonId ||
                form.selectedTools.size === 0 ||
                execModeRequired
              }
              loading={submitting}
            >
              Create connector
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setShowCreate(false)}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}

      {/* Activity log. Refused attempts are shown alongside successful ones:
          a burst of denials is the signal worth seeing. */}
      <div className="border border-border/40 rounded-lg p-6 space-y-3">
        <div className="flex items-center justify-between">
          <h3 className="font-medium">Recent activity</h3>
          {deniedCount > 0 && (
            <Badge variant="destructive">{deniedCount} blocked</Badge>
          )}
        </div>

        {activity.length === 0 ? (
          <p className="text-sm text-muted-foreground">No activity yet.</p>
        ) : (
          <div className="space-y-1 max-h-80 overflow-y-auto">
            {activity.map((a) => (
              <div
                key={a.id}
                className="flex items-start justify-between gap-3 text-xs py-1.5 border-b border-border/20 last:border-0"
              >
                <div className="min-w-0">
                  <span
                    className={cn(
                      "font-mono",
                      a.denied && "text-red-600 dark:text-red-400"
                    )}
                  >
                    {a.toolName}
                  </span>
                  {a.arguments && a.arguments !== "{}" && (
                    <span className="text-muted-foreground ml-2 truncate inline-block max-w-md align-bottom">
                      {a.arguments}
                    </span>
                  )}
                  {a.denied && a.errorMessage && (
                    <p className="text-red-600 dark:text-red-400 mt-0.5">
                      Blocked: {a.errorMessage}
                    </p>
                  )}
                  {a.status === "started" && (
                    <p className="text-yellow-700 dark:text-yellow-400 mt-0.5">
                      Outcome unknown — the server stopped before this call
                      finished, so it may or may not have run.
                    </p>
                  )}
                </div>
                <span className="text-muted-foreground shrink-0">
                  {formatDate(a.createdAt)}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
