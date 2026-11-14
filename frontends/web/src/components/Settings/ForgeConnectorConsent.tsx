import { useEffect, useMemo, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { useQueryClient } from "@tanstack/react-query";
import { Check, Loader2, Plus } from "lucide-react";
import {
  AuthorizeClientRequestSchema,
  ConnectorKind,
  CreateConnectorRequestSchema,
} from "../../gen/reliant/v1/connector_pb";
import type { Connector, ConnectorTool } from "../../gen/reliant/v1/connector_pb";
import {
  connectorServiceQueries,
  useAuthorizeClient,
} from "../../hooks/connector-service-hooks_gen";
import { useActiveOrg } from "../../hooks/useOrgs";
import Input from "../forge-ui/input";
import Button from "@/components/forge-ui/button";
import Badge from "../forge-ui/badge";
import { cn } from "../../lib/utils";

/**
 * Consent for the Reliant Forge connector (/forge/mcp): which organization,
 * which projects and which forge tools an application may use as you.
 *
 * Unlike a workspace connector this reaches no machine. The application acts
 * as you at the control plane, so it can never see more than you can; the
 * grant chosen here narrows that to one organization, optionally some of its
 * projects, and the tools ticked below. Mutating tools (obs_init,
 * release_record) are labelled: the application's client asks before calling
 * them too.
 */
export function ForgeConnectorConsent({
  clientId,
  clientName,
  onDone,
  onCancel,
  submitLabel,
  busy = false,
}: {
  clientId: string;
  clientName?: string;
  onDone?: () => void;
  onCancel?: () => void;
  submitLabel?: string;
  busy?: boolean;
}) {
  const queryClient = useQueryClient();
  const authorizeClient = useAuthorizeClient();
  const { orgs, activeOrgId, isLoading: orgsLoading } = useActiveOrg();

  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [tools, setTools] = useState<ConnectorTool[]>([]);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [authorized, setAuthorized] = useState(false);
  const [mode, setMode] = useState<"existing" | "new">("existing");
  const [selectedConnector, setSelectedConnector] = useState("");

  const [name, setName] = useState(clientName ? `${clientName} (Forge)` : "Reliant Forge");
  const [orgId, setOrgId] = useState("");
  const [allProjects, setAllProjects] = useState(true);
  const [projects, setProjects] = useState("");
  const [selectedTools, setSelectedTools] = useState<Set<string>>(new Set());

  const displayName = clientName?.trim() || clientId || "This application";

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [connectorsRes, toolsRes] = await Promise.all([
          queryClient.fetchQuery(connectorServiceQueries.listConnectors({}, { staleTime: 0 })),
          queryClient.fetchQuery(
            connectorServiceQueries.listAvailableTools({ kind: ConnectorKind.FORGE }),
          ),
        ]);
        if (cancelled) return;
        const live = connectorsRes.connectors.filter(
          (c) => !c.revokedAt && c.kind === ConnectorKind.FORGE,
        );
        setConnectors(live);
        setTools(toolsRes.tools);
        // Every tool, mutating ones included: the grant is a ceiling, and the
        // client still asks before a mutating call.
        setSelectedTools(new Set(toolsRes.tools.map((t) => t.name)));
        if (live.length === 1) setSelectedConnector(live[0].id);
        if (live.length === 0) setMode("new");
      } catch (err) {
        console.error("Failed to load forge consent data:", err);
        if (!cancelled) setError("Could not load your connectors.");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [queryClient]);

  // Default the organization to the one this session acts in.
  useEffect(() => {
    if (!orgId && activeOrgId) setOrgId(activeOrgId);
  }, [orgId, activeOrgId]);

  const projectList = useMemo(
    () =>
      projects
        .split(",")
        .map((p) => p.trim())
        .filter(Boolean),
    [projects],
  );

  const orgName = (id: string) => orgs.find((o) => o.id === id)?.name ?? id;

  const canSubmit =
    mode === "existing"
      ? Boolean(selectedConnector)
      : Boolean(name.trim()) &&
        Boolean(orgId) &&
        selectedTools.size > 0 &&
        (allProjects || projectList.length > 0);

  const toggleTool = (tool: string) =>
    setSelectedTools((prev) => {
      const next = new Set(prev);
      if (next.has(tool)) next.delete(tool);
      else next.add(tool);
      return next;
    });

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
                kind: ConnectorKind.FORGE,
                name: name.trim(),
                orgId,
                projectNames: allProjects ? [] : projectList,
                allowedTools: Array.from(selectedTools),
              }),
            });
      await authorizeClient.mutateAsync(req);
      if (onDone) {
        onDone();
        return;
      }
      setAuthorized(true);
    } catch (err) {
      console.error("Failed to authorize forge client:", err);
      setError(err instanceof Error ? err.message : "Could not authorize.");
    } finally {
      setSubmitting(false);
    }
  };

  if (loading || orgsLoading) {
    return (
      <div className="flex items-center justify-center py-16">
        <Loader2 className="w-5 h-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (authorized) {
    return (
      <div className="max-w-lg mx-auto py-16 text-center space-y-4">
        <div className="mx-auto w-12 h-12 rounded-full bg-success/10 flex items-center justify-center">
          <Check className="w-6 h-6 text-success-ink" />
        </div>
        <h2 className="text-lg font-semibold">{displayName} is connected to Reliant Forge</h2>
        <p className="text-sm text-muted-foreground">
          You can return to {displayName} and try again. Narrow or revoke this access any time
          from Settings → Access tokens (Reliant Forge connectors).
        </p>
      </div>
    );
  }

  return (
    <div className="max-w-2xl mx-auto py-8 space-y-6">
      <div>
        <h1 className="text-xl font-semibold mb-2">
          Allow {displayName} to use Reliant Forge?
        </h1>
        <p className="text-sm text-muted-foreground">
          {displayName} will read your apps&apos; monitoring, releases and environments as you
          — never more than you can see — in the organization and projects you choose here. It
          acts on text it reads, so grant only what it needs.
        </p>
      </div>

      {error && (
        <div className="rounded-lg border border-destructive/20 bg-destructive/10 p-3">
          <p className="text-sm text-destructive-ink">{error}</p>
        </div>
      )}

      {connectors.length > 0 && (
        <div className="flex gap-2">
          <Button
            variant={mode === "existing" ? "primary" : "outline"}
            size="sm"
            onClick={() => setMode("existing")}
          >
            Use an existing Forge connector
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
                selectedConnector === c.id ? "border-primary" : "border-border/40",
              )}
            >
              <input
                type="radio"
                name="forge-connector"
                className="mt-1"
                checked={selectedConnector === c.id}
                onChange={() => setSelectedConnector(c.id)}
              />
              <span className="min-w-0">
                <span className="font-medium">{c.name}</span>
                <span className="block text-xs text-muted-foreground truncate">
                  {orgName(c.orgId)} ·{" "}
                  {c.projectNames.length > 0 ? c.projectNames.join(", ") : "every project"} ·{" "}
                  {c.allowedTools.length} tools
                </span>
              </span>
            </label>
          ))}
        </div>
      )}

      {mode === "new" && (
        <div className="border border-border/40 rounded-lg p-6 space-y-5">
          <h2 className="font-medium">New Forge connector</h2>

          <div className="space-y-2">
            <label className="text-sm font-medium" htmlFor="forge-connector-name">
              Name
            </label>
            <Input
              id="forge-connector-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="forge-ui"
            />
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium" htmlFor="forge-connector-org">
              Organization
            </label>
            <select
              id="forge-connector-org"
              className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
              value={orgId}
              onChange={(e) => setOrgId(e.target.value)}
            >
              <option value="">Select an organization…</option>
              {orgs.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.name}
                </option>
              ))}
            </select>
          </div>

          <div className="space-y-2">
            <span className="text-sm font-medium">Projects</span>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="radio"
                name="forge-projects"
                checked={allProjects}
                onChange={() => setAllProjects(true)}
              />
              Every project in {orgId ? orgName(orgId) : "the organization"}
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="radio"
                name="forge-projects"
                checked={!allProjects}
                onChange={() => setAllProjects(false)}
              />
              Only these projects
            </label>
            {!allProjects && (
              <Input
                aria-label="Project names"
                placeholder="shop, shop-api"
                value={projects}
                onChange={(e) => setProjects(e.target.value)}
                className="forge-ui"
              />
            )}
            <p className="text-xs text-muted-foreground">
              A project is a forge project name: the &lt;project&gt; in
              forge.reliantlabs.io/forge/p/&lt;project&gt;.
            </p>
          </div>

          <div className="space-y-2">
            <span className="text-sm font-medium">Tools</span>
            <div className="space-y-1">
              {tools.map((t) => (
                <label key={t.name} className="flex items-start gap-2 text-sm">
                  <input
                    type="checkbox"
                    className="mt-1"
                    checked={selectedTools.has(t.name)}
                    onChange={() => toggleTool(t.name)}
                  />
                  <span className="min-w-0">
                    <span className="flex items-center gap-2">
                      <span className="font-mono">{t.name}</span>
                      {t.mutating && <Badge variant="warning" label="Changes things" />}
                    </span>
                    <span className="block text-xs text-muted-foreground">{t.description}</span>
                  </span>
                </label>
              ))}
            </div>
          </div>
        </div>
      )}

      <div className="flex items-center gap-2">
        <Button
          variant="primary"
          onClick={handleAuthorize}
          disabled={!canSubmit || submitting || busy}
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
