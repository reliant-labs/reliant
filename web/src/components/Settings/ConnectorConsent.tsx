import { useState, useEffect, useCallback, useMemo } from "react";
import { create } from "@bufbuild/protobuf";
import { Check, Loader2, ShieldAlert, Plus, Github } from "lucide-react";
import { grpcClient } from "../../api/grpc-client";
import {
  ListConnectorsRequestSchema,
  ListAvailableToolsRequestSchema,
  AuthorizeClientRequestSchema,
  CreateConnectorRequestSchema,
  ConnectorExecMode,
} from "../../gen/reliant/v1/connector_pb";
import type {
  Connector,
  ConnectorTool,
} from "../../gen/reliant/v1/connector_pb";
import { ListDaemonsRequestSchema } from "../../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../../gen/reliant/v1/daemon_registry_pb";
import { RepoSelector } from "../Projects/RepoSelector";
import type { GitRepo } from "../../services/controlPlane/git";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Badge } from "../ui/Badge";
import { cn } from "../../lib/utils";

/**
 * OAuth consent: choosing which connector an application may act through.
 *
 * An OAuth token identifies the USER, not what they meant to share. This page
 * is where that gap is closed — the user either points the application at an
 * existing connector or creates one for it.
 *
 * The create path reuses the same fields as the settings editor rather than
 * offering a simplified "approve" button. The scope of the grant IS the
 * consent decision: a screen that hides which workspace, which tools, and
 * whether shell access is included would be asking someone to approve
 * something they cannot see.
 */
/**
 * True when a daemon runs on the user's own computer rather than a disposable
 * cloud sandbox.
 *
 * The distinction drives what a permissive grant actually costs. On a managed
 * workspace the pod is the blast radius and it is rebuilt on demand; on a
 * personal machine there is no boundary underneath the policy, so the same
 * grant reaches real files. `daemon_type` is set at registration by the daemon
 * registry; anything unrecognized is treated as personal, because guessing
 * "sandbox" for an unknown machine is the dangerous direction.
 */
function isPersonalMachine(d: DaemonInfo): boolean {
  return d.daemonType !== "managed";
}

/** The machines this user can bind a connector to, from reliant's registry. */
async function fetchDaemons(): Promise<DaemonInfo[]> {
  const res = await grpcClient
    .daemonRegistry()
    .listDaemons(create(ListDaemonsRequestSchema, {}));
  return res.daemons;
}

/**
 * How long to wait for a freshly-created machine to become bindable.
 *
 * Creation goes through the control plane, which provisions a pod; the machine
 * only becomes selectable here once it has registered with reliant. Those are
 * two different systems, so the id is not usable the moment CreateDaemon
 * returns and the form has to wait for it to show up.
 */
const PROVISION_TIMEOUT_MS = 180_000;
const PROVISION_POLL_MS = 2_000;

/**
 * Poll timings, overridable so tests do not sit through real provisioning
 * delays. Production never passes these.
 */
export interface ProvisionTiming {
  pollMs?: number;
  timeoutMs?: number;
}

// Numeric protobuf enum values for a "managed small" machine, matching the
// shape `createDaemon` takes (see services/controlPlane/daemon.ts, which keeps
// these as numbers so callers need not import the control-plane enums).
const DAEMON_TYPE_MANAGED_ENUM = 1;
const DAEMON_SIZE_SMALL_ENUM = 1;

export function ConnectorConsent({
  clientId,
  clientName,
  onDone,
  onCancel,
  submitLabel,
  busy = false,
  provisionTiming,
}: {
  clientId: string;
  clientName?: string;
  onDone?: () => void;
  /** Test-only poll timing override; see ProvisionTiming. */
  provisionTiming?: ProvisionTiming;
  /** Cancel handler. Defaults to browser-back for the standalone settings route. */
  onCancel?: () => void;
  /** Overrides the submit button label. */
  submitLabel?: string;
  /**
   * Keeps the submit button in its loading state while the caller finishes
   * work of its own after `onDone`. Inside the OAuth flow that work is the
   * Supabase approval and redirect, so releasing the button when the binding
   * is saved would flash "done" on a flow that is still running.
   */
  busy?: boolean;
}) {
  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [tools, setTools] = useState<ConnectorTool[]>([]);
  const [daemons, setDaemons] = useState<DaemonInfo[]>([]);

  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [authorized, setAuthorized] = useState(false);
  const [creatingMachine, setCreatingMachine] = useState(false);
  const [githubConnected, setGithubConnected] = useState(false);
  const [githubLoading, setGithubLoading] = useState(true);
  const [showRepoPicker, setShowRepoPicker] = useState(false);
  const [cloning, setCloning] = useState(false);
  const [clonedRepo, setClonedRepo] = useState<string | null>(null);

  // "existing" until the user has no connectors, in which case creating one is
  // the only path forward and defaulting to it saves a click.
  const [mode, setMode] = useState<"existing" | "new">("existing");
  const [selectedConnector, setSelectedConnector] = useState("");

  // Defaults are fully permissive: every tool, the whole filesystem, and
  // unrestricted shell. The grant is a ceiling rather than a prediction —
  // a connector that cannot do what the user asks reads as a broken product,
  // and the narrowing controls are all on screen for anyone who wants them.
  //
  // The trade is real and deliberate: a connector is driven by a model reading
  // untrusted text, so these defaults hand that model the daemon's full reach.
  // On a managed sandbox the pod is the boundary; on a personal machine there
  // is none, which is why selecting one raises a warning beside this form.
  const [form, setForm] = useState({
    name: clientName ? `${clientName}` : "New connector",
    daemonId: "",
    pathRoot: "/",
    selectedTools: new Set<string>(),
    execMode: ConnectorExecMode.UNRESTRICTED,
    execAllowlist: "git, go, npm",
  });

  const displayName = clientName?.trim() || clientId;

  const load = useCallback(async () => {
    try {
      setError(null);
      const client = grpcClient.connector();
      const [connectorsRes, toolsRes] = await Promise.all([
        client.listConnectors(create(ListConnectorsRequestSchema, {})),
        client.listAvailableTools(create(ListAvailableToolsRequestSchema, {})),
      ]);

      const live = connectorsRes.connectors.filter((c) => !c.revokedAt);
      setConnectors(live);
      setTools(toolsRes.tools);

      // Preselect only when there is exactly one — with several, the choice is
      // the point of this screen and preselecting would invite approving
      // whichever happened to be first.
      if (live.length === 1) {
        setSelectedConnector(live[0].id);
      }
      if (live.length === 0) {
        setMode("new");
      }

      try {
        setDaemons(await fetchDaemons());
      } catch {
        setDaemons([]);
      }
    } catch (err) {
      console.error("Failed to load consent data:", err);
      setError("Could not load your connectors.");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // Select every tool once the catalog arrives, including the mutating ones.
  // Only runs while the selection is untouched, so it cannot re-tick a box the
  // user just cleared.
  useEffect(() => {
    if (tools.length > 0 && form.selectedTools.size === 0) {
      setForm((f) => ({
        ...f,
        selectedTools: new Set(tools.map((t) => t.name)),
      }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tools]);

  const selectedNeedsExec = useMemo(
    () => tools.some((t) => form.selectedTools.has(t.name) && t.needsExec),
    [tools, form.selectedTools]
  );

  const selectedDaemon = useMemo(
    () => daemons.find((d) => d.daemonId === form.daemonId),
    [daemons, form.daemonId]
  );
  const selectedIsPersonal = selectedDaemon
    ? isPersonalMachine(selectedDaemon)
    : false;

  // Directories the selected machine already reports, offered as one-click
  // roots so the common case is not "type an absolute path from memory".
  const suggestedRoots = useMemo(() => {
    const paths = (selectedDaemon?.projects ?? [])
      .map((p) => p.path?.trim())
      .filter((p): p is string => Boolean(p));
    return Array.from(new Set(paths)).slice(0, 6);
  }, [selectedDaemon]);

  const allSelected =
    tools.length > 0 && tools.every((t) => form.selectedTools.has(t.name));

  /**
   * Provision a new cloud machine and select it.
   *
   * Without this, a user with no machines reaches a dropdown with nothing in
   * it and the flow dead-ends one step short of working — the same shape of
   * failure the combined consent flow exists to remove.
   *
   * A created machine is always `managed`: a disposable per-user sandbox where
   * the pod is a real boundary. That matters given the permissive defaults
   * above, which are safe there in a way they are not on someone's laptop.
   */
  const handleCreateMachine = async () => {
    setCreatingMachine(true);
    setError(null);
    try {
      const known = new Set(daemons.map((d) => d.daemonId));

      const { createDaemon } = await import("../../services/controlPlane/daemon");
      const createdID = await createDaemon({
        name: clientName ? `${clientName} workspace` : "connector workspace",
        daemonType: DAEMON_TYPE_MANAGED_ENUM,
        size: DAEMON_SIZE_SMALL_ENUM,
      });

      // CreateDaemon returns before the machine has registered with reliant,
      // so poll for the row rather than assuming it is immediately bindable.
      const pollMs = provisionTiming?.pollMs ?? PROVISION_POLL_MS;
      const deadline =
        Date.now() + (provisionTiming?.timeoutMs ?? PROVISION_TIMEOUT_MS);
      for (;;) {
        await new Promise((r) => setTimeout(r, pollMs));

        let current: DaemonInfo[] = [];
        try {
          current = await fetchDaemons();
        } catch {
          // A transient list failure during provisioning is not fatal; the
          // next tick retries until the deadline.
        }

        // Match the machine we actually created. Falling back to "any row I
        // had not seen" is only for a server that declined to name it — that
        // heuristic previously bound the connector to a stale daemon from
        // weeks earlier, which then failed every call.
        const fresh = createdID
          ? current.find((d) => d.daemonId === createdID)
          : current.find((d) => !known.has(d.daemonId));
        if (fresh) {
          setDaemons(current);
          setForm((f) => ({ ...f, daemonId: fresh.daemonId }));
          return;
        }

        if (Date.now() > deadline) {
          setDaemons(current);
          setError(
            "The new machine is taking longer than expected to start. It may still " +
              "appear in the list shortly — reload this page, or pick another machine."
          );
          return;
        }
      }
    } catch (err) {
      const { isReasonedQuotaError } = await import(
        "../../hooks/useOnboardingQueries"
      );
      // A quota refusal already opened the upgrade modal globally; a second
      // inline banner under it would just be noise.
      if (isReasonedQuotaError(err)) return;
      console.error("Failed to create machine:", err);
      setError(
        err instanceof Error ? err.message : "Could not create a new machine."
      );
    } finally {
      setCreatingMachine(false);
    }
  };

  // Whether the user has a GitHub credential at all. Checked once: it is a
  // property of the account, not of the machine picked below.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const { gitService } = await import("../../services/controlPlane/git");
        const res = await gitService.getCredential("github");
        if (!cancelled) setGithubConnected(res.hasToken);
      } catch {
        // Treat an unreadable credential as "not connected". Offering the
        // button needlessly is a smaller failure than hiding the one thing
        // that explains why git does not work.
        if (!cancelled) setGithubConnected(false);
      } finally {
        if (!cancelled) setGithubLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  /**
   * Send the user through the GitHub OAuth flow, returning here afterwards.
   *
   * returnTo carries the full current URL — including the authorization_id
   * when this form is embedded in the OAuth consent flow — so the round trip
   * resumes the authorization instead of stranding it. Losing that parameter
   * would leave the third-party client waiting on a code that never comes.
   */
  const handleConnectGitHub = async () => {
    setError(null);
    try {
      const [{ gitService }, { supabase }] = await Promise.all([
        import("../../services/controlPlane/git"),
        import("../../lib/supabase"),
      ]);

      const oauthURL = gitService.getOAuthURL();
      if (!oauthURL) {
        setError("GitHub connections are not available in this deployment.");
        return;
      }

      const {
        data: { session },
      } = await supabase.auth.getSession();
      if (!session?.access_token) {
        setError("You need to be signed in to connect GitHub.");
        return;
      }

      const returnTo = `${window.location.pathname}${window.location.search}`;
      const params = new URLSearchParams({
        token: session.access_token,
        returnTo,
      });
      window.location.href = `${oauthURL}?${params.toString()}`;
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Could not start the GitHub connection."
      );
    }
  };

  /**
   * Clone the chosen repository onto the selected machine.
   *
   * Non-fatal by design. The connector grant is the thing being authorized
   * here; a checkout is a convenience, and a clone that fails should not cost
   * the user the authorization they came for. CloneRepo also queues via
   * JetStream when the machine is still starting, so "success" can mean
   * "delivered or queued" rather than "on disk right now" — worth saying, and
   * the reason the confirmation avoids claiming the files are ready.
   */
  const handleCloneRepo = async (repo: GitRepo) => {
    if (!form.daemonId) return;
    setCloning(true);
    setError(null);
    try {
      const [{ gitService }, { cloudPathForRepo }] = await Promise.all([
        import("../../services/controlPlane/git"),
        import("../../lib/cloudProjectPath"),
      ]);

      await gitService.cloneRepo({
        daemonId: form.daemonId,
        gitRepo: repo.cloneUrl,
        gitBranch: repo.defaultBranch || "main",
        path: cloudPathForRepo(repo),
      });

      setClonedRepo(repo.fullName);
      setShowRepoPicker(false);
    } catch (err) {
      setError(
        `Could not clone ${repo.fullName}: ` +
          (err instanceof Error ? err.message : "unknown error") +
          ". You can still continue — the connector will just start with an empty machine."
      );
    } finally {
      setCloning(false);
    }
  };

  const toggleAllTools = () => {
    setForm((f) => ({
      ...f,
      selectedTools: allSelected
        ? new Set<string>()
        : new Set(tools.map((t) => t.name)),
    }));
  };
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

  const handleAuthorize = async () => {
    setSubmitting(true);
    setError(null);
    try {
      const req =
        mode === "existing"
          ? create(AuthorizeClientRequestSchema, {
              clientId,
              clientName: clientName ?? "",
              connectorId: selectedConnector,
            })
          : create(AuthorizeClientRequestSchema, {
              clientId,
              clientName: clientName ?? "",
              newConnector: create(CreateConnectorRequestSchema, {
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
              }),
            });

      await grpcClient.connector().authorizeClient(req);
      // The grant and binding now exist. Inside the OAuth flow the caller
      // takes over from here (approve, then redirect), so the "connected"
      // screen below would be a dead end shown over a live flow — leave the
      // form in place and let the caller navigate.
      if (onDone) {
        onDone();
        return;
      }
      setAuthorized(true);
    } catch (err) {
      console.error("Failed to authorize client:", err);
      setError(err instanceof Error ? err.message : "Could not authorize.");
    } finally {
      setSubmitting(false);
    }
  };

  const canSubmit =
    mode === "existing"
      ? Boolean(selectedConnector)
      : Boolean(form.name.trim()) &&
        Boolean(form.daemonId) &&
        form.selectedTools.size > 0 &&
        !execModeRequired;

  if (loading) {
    return (
      <div className="flex items-center justify-center py-16">
        <Loader2 className="w-5 h-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (authorized) {
    return (
      <div className="max-w-lg mx-auto py-16 text-center space-y-4">
        <div className="mx-auto w-12 h-12 rounded-full bg-green-100 dark:bg-green-950/40 flex items-center justify-center">
          <Check className="w-6 h-6 text-green-700 dark:text-green-400" />
        </div>
        <h2 className="text-lg font-semibold">{displayName} is connected</h2>
        <p className="text-sm text-muted-foreground">
          You can return to {displayName} and try again. Manage or revoke this
          access any time from Settings → Connectors.
        </p>
      </div>
    );
  }

  return (
    <div className="max-w-2xl mx-auto py-8 space-y-6">
      <div>
        <h1 className="text-xl font-semibold mb-2">
          Allow {displayName} to use a machine?
        </h1>
        <p className="text-sm text-muted-foreground">
          {displayName} will be able to run the tools you choose on the machine
          you pick, on your behalf. It acts on text it reads — including from
          web pages and repositories — so grant only what it needs.
        </p>
      </div>

      {error && (
        <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-3">
          <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
        </div>
      )}

      {connectors.length > 0 && (
        <div className="flex gap-2">
          <Button
            variant={mode === "existing" ? "primary" : "outline"}
            size="sm"
            onClick={() => setMode("existing")}
          >
            Use an existing connector
          </Button>
          <Button
            variant={mode === "new" ? "primary" : "outline"}
            size="sm"
            onClick={() => setMode("new")}
            leftIcon={<Plus className="w-4 h-4" />}
          >
            Create a new one
          </Button>
        </div>
      )}

      {mode === "existing" && (
        <div className="border border-border/40 rounded-lg p-6 space-y-3">
          <h2 className="font-medium">Choose a connector</h2>
          {connectors.map((c) => (
            <label
              key={c.id}
              className={cn(
                "flex items-start gap-3 border rounded-lg p-3 cursor-pointer hover:bg-muted/40",
                selectedConnector === c.id
                  ? "border-primary"
                  : "border-border/40"
              )}
            >
              <input
                type="radio"
                name="connector"
                className="mt-1"
                checked={selectedConnector === c.id}
                onChange={() => setSelectedConnector(c.id)}
              />
              <span className="min-w-0">
                <span className="flex items-center gap-2">
                  <span className="font-medium">{c.name}</span>
                  {c.execMode !== ConnectorExecMode.DENY && (
                    <Badge variant="warning">Shell</Badge>
                  )}
                </span>
                <span className="block text-xs text-muted-foreground font-mono truncate">
                  {c.pathRoot} · {c.allowedTools.length} tools
                </span>
              </span>
            </label>
          ))}
        </div>
      )}

      {mode === "new" && (
        <div className="border border-border/40 rounded-lg p-6 space-y-5">
          <h2 className="font-medium">New connector</h2>

          <div className="space-y-2">
            <label className="text-sm font-medium">Name</label>
            <Input
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium">Machine</label>
            {daemons.length > 0 && (
              <select
                className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                value={form.daemonId}
                onChange={(e) => setForm({ ...form, daemonId: e.target.value })}
                disabled={creatingMachine}
              >
                <option value="">Select a machine…</option>
                {daemons.map((d) => (
                  <option key={d.daemonId} value={d.daemonId}>
                    {d.hostname || d.daemonId}
                    {isPersonalMachine(d) ? " (your computer)" : ""}
                  </option>
                ))}
              </select>
            )}

            {/* Having no machine at all used to end the flow here. Creating one
                is offered inline so the answer to "nothing to select" is a
                button rather than a dead end. */}
            {daemons.length === 0 && !creatingMachine && (
              <p className="text-sm text-muted-foreground">
                You have no machines yet. Create a cloud one to continue.
              </p>
            )}

            {creatingMachine ? (
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <Loader2 className="w-4 h-4 animate-spin" />
                Starting a new cloud machine — this usually takes under a
                minute.
              </div>
            ) : (
              <Button
                variant="outline"
                size="sm"
                onClick={handleCreateMachine}
                leftIcon={<Plus className="w-4 h-4" />}
              >
                {daemons.length === 0
                  ? "Create a cloud machine"
                  : "Create a new cloud machine"}
              </Button>
            )}

            <p className="text-xs text-muted-foreground">
              {displayName} reaches exactly this one machine, and nothing
              outside it.
            </p>

            {/* Git access is INHERITED from the machine, not granted by this
                connector: there are no git tools in the catalog, so anything
                git-shaped runs through the shell against whatever credentials
                and checkouts the machine already holds.
                
                Two distinct gaps, and a connected account only closes the
                first. A machine with credentials but no checkout is still an
                empty directory — the model has nothing to read. So the repo
                picker is offered whenever a machine is selected, not only when
                GitHub is disconnected. */}
            {form.daemonId && !githubLoading && (
              <div className="rounded-md border border-border/60 p-3 space-y-3">
                {!githubConnected ? (
                  <div className="flex items-start gap-2">
                    <Github className="w-4 h-4 shrink-0 mt-0.5 text-muted-foreground" />
                    <div className="space-y-2 min-w-0">
                      <p className="text-xs text-muted-foreground">
                        No GitHub access is connected. {displayName} will be able
                        to read and change files here, but cannot clone private
                        repositories or push.
                      </p>
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={handleConnectGitHub}
                        leftIcon={<Github className="w-4 h-4" />}
                      >
                        Connect GitHub
                      </Button>
                    </div>
                  </div>
                ) : clonedRepo ? (
                  <div className="flex items-center gap-2">
                    <Check className="w-4 h-4 shrink-0 text-green-600 dark:text-green-400" />
                    <p className="text-xs text-muted-foreground min-w-0 truncate">
                      Cloned{" "}
                      <span className="font-mono">{clonedRepo}</span> onto this
                      machine.
                    </p>
                  </div>
                ) : (
                  <div className="space-y-2">
                    <div className="flex items-center justify-between gap-2">
                      <p className="text-xs text-muted-foreground">
                        Optionally clone a repository so {displayName} has
                        something to work on.
                      </p>
                      {!showRepoPicker && (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => setShowRepoPicker(true)}
                          leftIcon={<Github className="w-4 h-4" />}
                        >
                          Clone a repo
                        </Button>
                      )}
                    </div>

                    {cloning && (
                      <div className="flex items-center gap-2 text-xs text-muted-foreground">
                        <Loader2 className="w-4 h-4 animate-spin" />
                        Cloning — this continues in the background if the
                        machine is still starting.
                      </div>
                    )}

                    {showRepoPicker && !cloning && (
                      <div className="max-h-72 overflow-y-auto">
                        {/* oauthReturnTo keeps an in-flight authorization_id
                            across a reconnect, so the OAuth flow this form is
                            embedded in survives the round trip. */}
                        <RepoSelector
                          onSelect={handleCloneRepo}
                          oauthReturnTo={`${window.location.pathname}${window.location.search}`}
                          analyticsPhase="connector_consent"
                        />
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}
            {selectedIsPersonal && (
              <div className="flex gap-2 rounded-md border border-yellow-200 dark:border-yellow-800 bg-yellow-50 dark:bg-yellow-950/20 p-3">
                <ShieldAlert className="w-4 h-4 shrink-0 text-yellow-700 dark:text-yellow-400 mt-0.5" />
                <p className="text-xs text-yellow-800 dark:text-yellow-300">
                  This is your own computer, not a disposable sandbox. Anything
                  you allow here runs against your real files — prefer a narrow
                  directory and no shell access.
                </p>
              </div>
            )}
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium">Allowed directory</label>
            <Input
              value={form.pathRoot}
              onChange={(e) => setForm({ ...form, pathRoot: e.target.value })}
              className="font-mono text-sm"
              placeholder="/Users/you/code/project"
            />
            {(suggestedRoots.length > 0 || form.daemonId) && (
              <div className="flex flex-wrap gap-1.5">
                {suggestedRoots.map((path) => (
                  <button
                    key={path}
                    type="button"
                    onClick={() => setForm((f) => ({ ...f, pathRoot: path }))}
                    className="text-xs font-mono px-2 py-1 rounded border border-border/60 hover:bg-muted/60 truncate max-w-[18rem]"
                  >
                    {path}
                  </button>
                ))}
                {/* "Everything" is a real root rather than a wildcard: the
                    daemon confines fs.* by prefix, so the whole filesystem is
                    expressed as "/" — the one path every other path is under.
                    A literal "*" is not a path and is rejected server-side. */}
                <button
                  type="button"
                  onClick={() => setForm((f) => ({ ...f, pathRoot: "/" }))}
                  className="text-xs font-mono px-2 py-1 rounded border border-border/60 hover:bg-muted/60"
                >
                  Entire machine (/)
                </button>
              </div>
            )}
            {form.pathRoot.trim() === "/" && (
              <div className="flex gap-2 rounded-md border border-yellow-200 dark:border-yellow-800 bg-yellow-50 dark:bg-yellow-950/20 p-3">
                <ShieldAlert className="w-4 h-4 shrink-0 text-yellow-700 dark:text-yellow-400 mt-0.5" />
                <p className="text-xs text-yellow-800 dark:text-yellow-300">
                  {displayName} will be able to read every file this machine's
                  daemon can reach, including SSH keys, browser profiles, and
                  credentials outside any project.
                </p>
              </div>
            )}
          </div>

          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <label className="text-sm font-medium">Tools</label>
              <button
                type="button"
                onClick={toggleAllTools}
                className="text-xs text-muted-foreground hover:text-foreground underline"
              >
                {allSelected ? "Deselect all" : "Select all"}
              </button>
            </div>
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
                      {t.mutating && <Badge variant="warning">writes</Badge>}
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
              <option value={ConnectorExecMode.DENY}>No commands</option>
              <option value={ConnectorExecMode.ALLOWLIST}>
                Only specific programs
              </option>
              <option value={ConnectorExecMode.UNRESTRICTED}>
                Any command, through a shell
              </option>
            </select>

            {form.execMode === ConnectorExecMode.ALLOWLIST && (
              <Input
                value={form.execAllowlist}
                onChange={(e) =>
                  setForm({ ...form, execAllowlist: e.target.value })
                }
                placeholder="git, go, npm"
                className="font-mono text-sm"
              />
            )}

            {execModeRequired && (
              <p className="text-xs text-yellow-700 dark:text-yellow-400">
                You selected a tool that runs commands, so shell access cannot
                be “No commands”.
              </p>
            )}

            {form.execMode === ConnectorExecMode.UNRESTRICTED && (
              <div className="flex gap-2 rounded-md border border-yellow-200 dark:border-yellow-800 bg-yellow-50 dark:bg-yellow-950/20 p-3">
                <ShieldAlert className="w-4 h-4 shrink-0 text-yellow-700 dark:text-yellow-400 mt-0.5" />
                <p className="text-xs text-yellow-800 dark:text-yellow-300">
                  {displayName} could run any command in this workspace. Only
                  choose this for a workspace you are willing to have modified
                  or destroyed.
                </p>
              </div>
            )}
          </div>
        </div>
      )}

      <div className="flex items-center gap-2">
        <Button
          variant="primary"
          onClick={handleAuthorize}
          disabled={!canSubmit || submitting || busy || creatingMachine}
          loading={submitting || busy}
        >
          {submitLabel ?? `Allow ${displayName}`}
        </Button>
        <Button
          variant="ghost"
          onClick={onCancel ?? (() => window.history.back())}
          disabled={submitting || busy}
        >
          Cancel
        </Button>
      </div>
    </div>
  );
}
