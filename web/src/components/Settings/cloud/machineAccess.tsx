/**
 * Machine → Access: the outside AI apps (ChatGPT, Claude, and their mobile
 * apps) granted access to ONE machine.
 *
 * This used to be a standalone Settings → Connectors section with a machine
 * picker in its create form. A grant reaches exactly one machine, so it now
 * lives on that machine: the list is the machine's grants, and create binds to
 * the machine being viewed — there is no picker to get wrong.
 *
 * The create form is a consent screen, so it is built to make the scope of a
 * grant legible rather than to be quick to fill in: read-only tools by
 * default, shell access off unless deliberately enabled, and a directory the
 * grant is confined to.
 *
 * Every RPC here is reliant's own ConnectorService. Nothing touches the
 * control plane, so this works on a build without one — a self-hosted
 * machine can carry grants too.
 */
import { useId, useMemo, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Plug, Plus, ShieldAlert, Terminal, Trash2 } from "lucide-react";

import { cn } from "@/lib/utils";
import { grpcClient } from "@/api/grpc-client";
import {
  ConnectorExecMode,
  CreateConnectorRequestSchema,
  ListAvailableToolsRequestSchema,
  ListConnectorActivityRequestSchema,
  ListConnectorsRequestSchema,
  RevokeConnectorRequestSchema,
  type Connector,
  type ConnectorActivity,
  type ConnectorTool,
} from "@/gen/reliant/v1/connector_pb";
import { Badge, Button, Card, CardContent, CardHeader, CardInset, CardTitle } from "./ui";

// ── Data ────────────────────────────────────────────────────────────────────

/**
 * Every grant the user holds, across machines. ListConnectors is user-scoped
 * (there is no per-daemon filter), so the machine list's app count and each
 * detail view read this one query and filter it — one request, not one per row.
 */
export const connectorsQueryKey = ["connectors", "list"] as const;
const toolsQueryKey = ["connectors", "tools"] as const;
const activityQueryKey = (grantIds: string[]) => ["connectors", "activity", ...grantIds] as const;

async function listConnectors(): Promise<Connector[]> {
  const res = await grpcClient.connector().listConnectors(create(ListConnectorsRequestSchema, {}));
  return res.connectors;
}

export function useConnectors() {
  return useQuery({
    queryKey: connectorsQueryKey,
    queryFn: listConnectors,
    staleTime: 15_000,
  });
}

/** Grants that still reach their machine. A revoked grant does not. */
export function isActiveGrant(c: Pick<Connector, "revokedAt">): boolean {
  return !c.revokedAt;
}

/** Active grant count per daemon id, for the machine list's indicator. */
export function activeGrantCounts(connectors: Connector[] | undefined): Map<string, number> {
  const counts = new Map<string, number>();
  for (const c of connectors ?? []) {
    if (!isActiveGrant(c)) continue;
    counts.set(c.daemonId, (counts.get(c.daemonId) ?? 0) + 1);
  }
  return counts;
}

export function appCountLabel(n: number): string {
  return `${n} ${n === 1 ? "app" : "apps"}`;
}

// ── Presentation helpers ────────────────────────────────────────────────────

const inputCls =
  "w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring";

function formatDate(iso?: string): string {
  if (!iso) return "Never";
  return new Date(iso).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

function CopyField({ label, value }: { label: string; value: string }) {
  const id = useId();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="text-xs text-muted-foreground">
        {label}
      </label>
      <div className="flex items-center gap-2">
        <input id={id} value={value} readOnly className={cn(inputCls, "font-mono")} />
        <Button variant="outline" size="sm" onClick={copy} aria-label={`Copy ${label}`}>
          {copied ? <Check className="h-4 w-4 text-success" /> : <Copy className="h-4 w-4" />}
        </Button>
      </div>
    </div>
  );
}

// ── Component ───────────────────────────────────────────────────────────────

export interface MachineAccessProps {
  daemonId: string;
  /** Display name, used in the consent copy so the scope is named, not implied. */
  machineName: string;
  /**
   * True for a machine on the user's own hardware rather than a disposable
   * cloud sandbox. On a sandbox the pod is the blast radius; on a personal
   * machine there is nothing underneath the policy, so the form says so.
   */
  personal: boolean;
  /** Pre-filled allowed directory. Empty means the user must choose one. */
  defaultPathRoot: string;
  /** Directories the machine already reports, offered as one-click roots. */
  suggestedRoots?: string[];
}

export function MachineAccess({
  daemonId,
  machineName,
  personal,
  defaultPathRoot,
  suggestedRoots = [],
}: MachineAccessProps) {
  const qc = useQueryClient();
  const titleId = useId();
  const nameId = useId();
  const pathId = useId();
  const shellId = useId();
  const allowlistId = useId();

  const [error, setError] = useState<string | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  // Newly minted credential. Held in component state only — it is returned
  // exactly once and is unrecoverable after this view is dismissed.
  const [created, setCreated] = useState<{ credential: string; mcpUrl: string; name: string } | null>(null);

  const [name, setName] = useState("");
  const [pathRoot, setPathRoot] = useState(defaultPathRoot);
  // null = "the default selection", derived from the tool catalog below, so
  // the default is right whenever the catalog arrives without an effect.
  const [pickedTools, setPickedTools] = useState<Set<string> | null>(null);
  const [execMode, setExecMode] = useState<ConnectorExecMode>(ConnectorExecMode.DENY);
  const [execAllowlist, setExecAllowlist] = useState("git, go, npm");

  const connectorsQ = useConnectors();
  const grants = useMemo(
    () => (connectorsQ.data ?? []).filter((c) => c.daemonId === daemonId),
    [connectorsQ.data, daemonId],
  );
  const grantIds = useMemo(() => grants.map((g) => g.id), [grants]);

  const toolsQ = useQuery({
    queryKey: toolsQueryKey,
    queryFn: async () =>
      (await grpcClient.connector().listAvailableTools(create(ListAvailableToolsRequestSchema, {}))).tools,
    staleTime: 5 * 60_000,
    // Only the create form needs the catalog.
    enabled: showCreate,
  });
  const tools: ConnectorTool[] = useMemo(() => toolsQ.data ?? [], [toolsQ.data]);

  // Activity is read per grant rather than account-wide: an account-wide page
  // of 50 is dominated by whichever machine is busiest, and would silently
  // show this machine nothing.
  const activityQ = useQuery({
    queryKey: activityQueryKey(grantIds),
    queryFn: async () => {
      const client = grpcClient.connector();
      const pages = await Promise.all(
        grantIds.map((grantId) =>
          client.listConnectorActivity(create(ListConnectorActivityRequestSchema, { grantId, limit: 25 })),
        ),
      );
      return pages
        .flatMap((p) => p.activity)
        .sort((a, b) => (b.createdAt ?? "").localeCompare(a.createdAt ?? ""))
        .slice(0, 50);
    },
    enabled: grantIds.length > 0,
  });
  const activity: ConnectorActivity[] = grantIds.length > 0 ? (activityQ.data ?? []) : [];
  const grantNames = useMemo(() => new Map(grants.map((g) => [g.id, g.name])), [grants]);
  const deniedCount = activity.filter((a) => a.denied).length;

  // Least privilege that is still useful: every read-only tool. Widening is a
  // deliberate act, one checkbox at a time.
  const selectedTools = useMemo(
    () => pickedTools ?? new Set(tools.filter((t) => !t.mutating).map((t) => t.name)),
    [pickedTools, tools],
  );
  const selectedNeedsExec = tools.some((t) => selectedTools.has(t.name) && t.needsExec);
  // The server rejects a shell tool granted without an exec mode, because such
  // a grant would refuse every call to it. Say so here, not on submit.
  const execModeRequired = selectedNeedsExec && execMode === ConnectorExecMode.DENY;

  const toggleTool = (toolName: string) => {
    const next = new Set(selectedTools);
    if (next.has(toolName)) next.delete(toolName);
    else next.add(toolName);
    setPickedTools(next);
  };

  const invalidate = () => qc.invalidateQueries({ queryKey: ["connectors"] });

  const createMut = useMutation({
    mutationFn: async () =>
      grpcClient.connector().createConnector(
        create(CreateConnectorRequestSchema, {
          name: name.trim(),
          daemonId,
          allowedTools: Array.from(selectedTools),
          pathRoot: pathRoot.trim(),
          execMode,
          execAllowlist:
            execMode === ConnectorExecMode.ALLOWLIST
              ? execAllowlist
                  .split(",")
                  .map((s) => s.trim())
                  .filter(Boolean)
              : [],
        }),
      ),
    onSuccess: (res) => {
      setCreated({ credential: res.credential, mcpUrl: res.mcpUrl, name: name.trim() });
      setShowCreate(false);
      setName("");
      setError(null);
      invalidate();
    },
    onError: (err) => setError(errorMessage(err, "Failed to grant access.")),
  });

  const revokeMut = useMutation({
    mutationFn: async (id: string) =>
      grpcClient.connector().revokeConnector(create(RevokeConnectorRequestSchema, { id })),
    onSuccess: () => {
      setRevokingId(null);
      setError(null);
      invalidate();
    },
    onError: () => setError("Failed to revoke access."),
  });

  const canSubmit =
    !createMut.isPending &&
    name.trim() !== "" &&
    pathRoot.trim() !== "" &&
    selectedTools.size > 0 &&
    !execModeRequired;

  // Active grants first; revoked ones stay visible beneath them as a record.
  const orderedGrants = [...grants].sort((a, b) => Number(!isActiveGrant(a)) - Number(!isActiveGrant(b)));

  return (
    <Card role="region" aria-labelledby={titleId}>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="space-y-1">
            <CardTitle id={titleId} className="inline-flex items-center gap-2">
              <Plug className="h-4 w-4 text-muted-foreground" /> Access
            </CardTitle>
            <p className="text-xs text-muted-foreground">
              Outside AI apps — ChatGPT, Claude, and their mobile apps — that can run tools on this machine.
            </p>
          </div>
          {!showCreate && (
            <Button size="sm" onClick={() => setShowCreate(true)}>
              <Plus className="h-4 w-4" /> Give an app access
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </div>
        )}

        {created && (
          <CardInset padding="md" className="space-y-3">
            <p className="text-sm font-medium text-foreground">Access for “{created.name}” created</p>
            <CopyField label="Server URL" value={created.mcpUrl} />
            <CopyField label="Credential (Bearer token)" value={created.credential} />
            <p className="text-xs font-medium text-warning">
              This credential is shown only once. Copy it now — it cannot be retrieved later.
            </p>
            <Button variant="ghost" size="sm" onClick={() => setCreated(null)}>
              Done
            </Button>
          </CardInset>
        )}

        {showCreate && (
          <CardInset padding="md" className="space-y-5">
            <div className="space-y-1">
              <h4 className="text-sm font-semibold text-foreground">Give an app access</h4>
              <p className="text-xs text-muted-foreground">
                This grant reaches only <span className="font-medium text-foreground">{machineName}</span>. If it is
                ever misused, nothing outside this machine is exposed.
              </p>
            </div>

            {personal && (
              <div className="flex gap-2 rounded-md border border-warning/30 bg-warning/10 p-3">
                <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
                <p className="text-xs text-foreground">
                  This is your own computer, not a disposable sandbox. Anything you allow here runs against your real
                  files — prefer a narrow directory and no shell access.
                </p>
              </div>
            )}

            <div className="space-y-1.5">
              <label htmlFor={nameId} className="text-sm font-medium text-foreground">
                Name
              </label>
              <input
                id={nameId}
                className={inputCls}
                placeholder="ChatGPT on my phone"
                value={name}
                onChange={(e) => setName(e.target.value)}
                autoFocus
              />
            </div>

            <div className="space-y-1.5">
              <label htmlFor={pathId} className="text-sm font-medium text-foreground">
                Allowed directory
              </label>
              <input
                id={pathId}
                className={cn(inputCls, "font-mono")}
                placeholder="/Users/you/code/project"
                value={pathRoot}
                onChange={(e) => setPathRoot(e.target.value)}
              />
              {suggestedRoots.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {suggestedRoots.map((root) => (
                    <button
                      key={root}
                      type="button"
                      onClick={() => setPathRoot(root)}
                      className={cn(
                        "rounded-md border border-border px-2 py-0.5 font-mono text-xs text-muted-foreground hover:bg-muted hover:text-foreground",
                        pathRoot === root && "border-primary text-foreground",
                      )}
                    >
                      {root}
                    </button>
                  ))}
                </div>
              )}
              <p className="text-xs text-muted-foreground">
                File access is confined to this directory, including through symlinks.
              </p>
            </div>

            <fieldset className="space-y-1.5">
              <legend className="mb-1.5 text-sm font-medium text-foreground">Tools</legend>
              {toolsQ.isLoading ? (
                <p className="text-xs text-muted-foreground">Loading tools…</p>
              ) : toolsQ.error ? (
                <p className="text-xs text-destructive">Could not load the tool list.</p>
              ) : (
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  {tools.map((t) => (
                    <label
                      key={t.name}
                      className="flex cursor-pointer items-start gap-2 rounded-md border border-border/60 bg-card p-2 hover:bg-muted/40"
                    >
                      <input
                        type="checkbox"
                        className="mt-1 accent-primary"
                        checked={selectedTools.has(t.name)}
                        onChange={() => toggleTool(t.name)}
                      />
                      <span className="min-w-0">
                        <span className="flex items-center gap-1.5">
                          <span className="font-mono text-sm">{t.name}</span>
                          {t.mutating && <Badge label="writes" variant="warning" size="sm" />}
                        </span>
                        <span className="line-clamp-2 block text-xs text-muted-foreground">{t.description}</span>
                      </span>
                    </label>
                  ))}
                </div>
              )}
            </fieldset>

            <div className="space-y-1.5">
              <label htmlFor={shellId} className="text-sm font-medium text-foreground">
                Shell access
              </label>
              <select
                id={shellId}
                className={inputCls}
                value={execMode}
                onChange={(e) => setExecMode(Number(e.target.value) as ConnectorExecMode)}
              >
                <option value={ConnectorExecMode.DENY}>No commands</option>
                <option value={ConnectorExecMode.ALLOWLIST}>Only specific programs</option>
                <option value={ConnectorExecMode.UNRESTRICTED}>Any command, through a shell</option>
              </select>

              {execMode === ConnectorExecMode.ALLOWLIST && (
                <>
                  <label htmlFor={allowlistId} className="sr-only">
                    Allowed programs
                  </label>
                  <input
                    id={allowlistId}
                    className={cn(inputCls, "font-mono")}
                    value={execAllowlist}
                    onChange={(e) => setExecAllowlist(e.target.value)}
                    placeholder="git, go, npm"
                  />
                  <p className="text-xs text-muted-foreground">
                    Only these programs can run, and they run without a shell — so pipes, redirection, and chaining
                    are unavailable. This is the setting to use for a machine you care about.
                  </p>
                </>
              )}

              {execModeRequired && (
                <p className="text-xs text-warning">
                  You selected a tool that runs shell commands, so shell access cannot be “No commands”.
                </p>
              )}

              {execMode === ConnectorExecMode.UNRESTRICTED && (
                <div className="flex gap-2 rounded-md border border-warning/30 bg-warning/10 p-3">
                  <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
                  <p className="text-xs text-foreground">
                    Commands run through a shell, so the app can run anything on this machine — and it acts on text it
                    reads, including text from web pages and repositories. Only use this for a machine you are willing
                    to have modified or destroyed. Prefer “Only specific programs”, which runs without a shell.
                  </p>
                </div>
              )}
            </div>

            <div className="flex items-center gap-2 pt-1">
              <Button size="sm" onClick={() => createMut.mutate()} disabled={!canSubmit} isLoading={createMut.isPending}>
                Grant access
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setShowCreate(false)}>
                Cancel
              </Button>
            </div>
          </CardInset>
        )}

        {connectorsQ.isLoading ? (
          <p className="text-sm text-muted-foreground">Loading access…</p>
        ) : connectorsQ.error ? (
          <p className="text-sm text-destructive">Could not load which apps have access to this machine.</p>
        ) : orderedGrants.length === 0 ? (
          <p className="text-sm text-muted-foreground">No apps have access to this machine.</p>
        ) : (
          <ul className="divide-y divide-border rounded-md border border-border">
            {orderedGrants.map((c) => {
              const revoked = !isActiveGrant(c);
              return (
                <li
                  key={c.id}
                  className={cn("flex items-start justify-between gap-4 px-4 py-3", revoked && "opacity-60")}
                >
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="truncate text-sm font-medium text-foreground">{c.name}</span>
                      {revoked ? (
                        <Badge label="Revoked" variant="error" size="sm" />
                      ) : (
                        <Badge label="Active" variant="success" size="sm" />
                      )}
                      {c.execMode !== ConnectorExecMode.DENY && c.execMode !== ConnectorExecMode.UNSPECIFIED && (
                        <Badge
                          label={
                            <>
                              <Terminal className="h-3 w-3" /> Shell
                            </>
                          }
                          variant="warning"
                          size="sm"
                        />
                      )}
                    </div>
                    <p className="truncate font-mono text-xs text-muted-foreground">
                      {c.tokenPrefix}… · {c.pathRoot}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {c.allowedTools.length} tools · last used {formatDate(c.lastUsedAt)}
                    </p>
                  </div>

                  {!revoked && (
                    <div className="shrink-0">
                      {revokingId === c.id ? (
                        <div className="flex items-center gap-2">
                          <Button
                            variant="danger"
                            size="sm"
                            isLoading={revokeMut.isPending}
                            onClick={() => revokeMut.mutate(c.id)}
                          >
                            Confirm
                          </Button>
                          <Button variant="ghost" size="sm" onClick={() => setRevokingId(null)}>
                            Cancel
                          </Button>
                        </div>
                      ) : (
                        <Button variant="ghost" size="sm" onClick={() => setRevokingId(c.id)}>
                          <Trash2 className="h-4 w-4 text-destructive" /> Revoke
                        </Button>
                      )}
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        )}

        {/* Refused attempts are shown alongside successful ones: a burst of
            denials is the signal worth seeing. */}
        {grants.length > 0 && (
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Recent activity</h4>
              {deniedCount > 0 && <Badge label={`${deniedCount} blocked`} variant="error" size="sm" />}
            </div>
            {activity.length === 0 ? (
              <p className="text-sm text-muted-foreground">No activity yet.</p>
            ) : (
              <CardInset padding="none" className="max-h-80 overflow-y-auto">
                {activity.map((a) => (
                  <div
                    key={a.id}
                    className="flex items-start justify-between gap-3 border-b border-border/40 px-3 py-1.5 text-xs last:border-0"
                  >
                    <div className="min-w-0">
                      <span className={cn("font-mono", a.denied && "text-destructive")}>{a.toolName}</span>
                      {grants.length > 1 && (
                        <span className="ml-2 text-muted-foreground">{grantNames.get(a.grantId)}</span>
                      )}
                      {a.arguments && a.arguments !== "{}" && (
                        <span className="ml-2 inline-block max-w-md truncate align-bottom text-muted-foreground">
                          {a.arguments}
                        </span>
                      )}
                      {a.denied && a.errorMessage && (
                        <p className="mt-0.5 text-destructive">Blocked: {a.errorMessage}</p>
                      )}
                      {a.status === "started" && (
                        <p className="mt-0.5 text-warning">
                          Outcome unknown — the server stopped before this call finished, so it may or may not have
                          run.
                        </p>
                      )}
                    </div>
                    <span className="shrink-0 text-muted-foreground">{formatDate(a.createdAt)}</span>
                  </div>
                ))}
              </CardInset>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
