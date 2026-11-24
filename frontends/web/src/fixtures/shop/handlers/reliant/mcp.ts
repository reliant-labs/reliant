// Copyright (c) 2025 Reliant Labs

/**
 * MCPService: shop's one installed MCP server, for the Connectors page's MCP
 * tab. A real ListServers carries each server's config headers and env as
 * the user stored them (API keys, bearer tokens), which is why it declares no
 * NO_SIDE_EFFECTS (reliant#3466). This fixture's config is obviously fake:
 * an `.invalid` URL and a header value that says it is not a secret
 * (__tests__/appLayer.test.ts asserts both).
 *
 * App layer only (../../app.ts APP_LAYER_ANSWERS): the demo pack never
 * answers an MCP read.
 */
import { ConfigScope } from "@/gen/reliant/v1/common_pb";
import { MCPServerStatus, MCPService } from "@/gen/reliant/v1/mcp_pb";

import { serve } from "../serve";

/** The fixture's one header value: says what it is, so nobody mistakes it for a key. */
export const FAKE_MCP_HEADER = "fixture-not-a-secret";

const SHOP_DOCS = {
  name: "shop-docs",
  status: MCPServerStatus.MCP_SERVER_STATUS_HEALTHY,
  connected: true,
  healthy: true,
  config: { type: "http", url: "https://mcp.shop.invalid/docs", headers: { Authorization: `Bearer ${FAKE_MCP_HEADER}` }, args: [], env: [] },
  serverInfo: { name: "shop-docs", version: "0.4.2" },
  toolCount: 3,
  resourcesEnabled: false,
  promptsEnabled: false,
  enabled: true,
  scope: ConfigScope.PROJECT,
};

export function mcpHandlers() {
  return serve(MCPService, {
    listServers: () => ({ servers: [SHOP_DOCS], total: 1 }),
    listRecommended: () => ({ recommended: [], total: 0 }),
  });
}
