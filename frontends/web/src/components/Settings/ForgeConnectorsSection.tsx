import { useQuery } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import { ConnectorKind, RevokeConnectorRequestSchema } from "../../gen/reliant/v1/connector_pb";
import {
  connectorServiceQueries,
  useRevokeConnector,
} from "../../hooks/connector-service-hooks_gen";
import { useActiveOrg } from "../../hooks/useOrgs";
import Card from "../forge-ui/card";
import Button from "@/components/forge-ui/button";

/**
 * The hosted Reliant Forge MCP endpoint people paste into their client: the
 * production api host's /forge/mcp (internal/mcpserver ForgeMountPath).
 */
const FORGE_MCP_URL = "https://api.reliantapi.com/forge/mcp";

/**
 * The Reliant Forge connectors: applications that read this person's forge
 * monitoring, releases and environments over MCP (/forge/mcp). Each is a
 * grant chosen on the consent screen — an organization, optionally some
 * projects, and tools — and revoking it stops the application's next call.
 */
export function ForgeConnectorsSection() {
  const { orgs } = useActiveOrg();
  const connectorsQ = useQuery(connectorServiceQueries.listConnectors({}));
  const revoke = useRevokeConnector();
  const forge = (connectorsQ.data?.connectors ?? []).filter(
    (c) => c.kind === ConnectorKind.FORGE && !c.revokedAt,
  );
  const orgName = (id: string) => orgs.find((o) => o.id === id)?.name ?? id;

  return (
    <Card padding="none">
      <div className="p-4 space-y-3">
        <div>
          <h3 className="text-sm font-semibold">Reliant Forge connectors</h3>
          <p className="text-xs text-muted-foreground mt-1">
            Applications that read your forge monitoring and releases over MCP. Add{" "}
            <span className="font-mono">{FORGE_MCP_URL}</span>{" "}
            as a custom connector in Claude, ChatGPT, VS Code or Cursor and sign in; `forge skill
            load connect` has the steps.
          </p>
        </div>
        {forge.length === 0 ? (
          <p className="text-sm text-muted-foreground">No application is connected.</p>
        ) : (
          <ul className="space-y-2">
            {forge.map((c) => (
              <li
                key={c.id}
                className="flex items-start justify-between gap-3 rounded-md border border-border/60 p-3"
              >
                <div className="min-w-0">
                  <div className="text-sm font-medium">{c.name}</div>
                  <div className="text-xs text-muted-foreground truncate">
                    {orgName(c.orgId)} ·{" "}
                    {c.projectNames.length > 0 ? c.projectNames.join(", ") : "every project"} ·{" "}
                    {c.allowedTools.length} tools
                    {c.lastUsedAt ? ` · last used ${new Date(c.lastUsedAt).toLocaleString()}` : ""}
                  </div>
                </div>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={revoke.isPending}
                  onClick={() => revoke.mutate(create(RevokeConnectorRequestSchema, { id: c.id }))}
                >
                  Revoke
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}
