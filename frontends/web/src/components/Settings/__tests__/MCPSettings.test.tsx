import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MCPSettings, resetMCPSettingsCacheForTests } from "../MCPSettings";
import {
  ConfigScope,
  type MCPServer,
  type RecommendedServer,
} from "../../../api/mcp-grpc";

const listServersMock = vi.fn();
const listRecommendedMock = vi.fn();
const updatePreferencesMock = vi.fn();
const loadPreferencesMock = vi.fn();

vi.mock("../../../api/mcp-grpc", () => ({
  ConfigScope: {
    GLOBAL: 0,
    PROJECT: 1,
    PROJECT_LOCAL: 2,
  },
  mcpGrpc: {
    listServers: (...args: unknown[]) => listServersMock(...args),
    listRecommended: (...args: unknown[]) => listRecommendedMock(...args),
    installServer: vi.fn(),
    restartServer: vi.fn(),
    uninstallServer: vi.fn(),
    setServerEnabled: vi.fn(),
    moveServerScope: vi.fn(),
    getServerTools: vi.fn(),
    updateServerConfig: vi.fn(),
  },
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (
    selector: (state: { currentProject: { id: string } | null }) => unknown,
  ) => selector({ currentProject: { id: "project-test" } }),
}));

vi.mock("../../../store/preferencesStore", () => ({
  usePreferencesStore: () => ({
    preferences: { defaultMcpScope: ConfigScope.PROJECT },
    updatePreferences: updatePreferencesMock,
    loadPreferences: loadPreferencesMock,
    isLoading: false,
  }),
}));

vi.mock("../../../store/refetchStore", () => ({
  subscribeToRefetch: () => () => {},
}));

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
  },
}));

const baseRecommended: RecommendedServer[] = [
  {
    name: "github",
    displayName: "GitHub",
    description: "GitHub tools",
    category: "dev",
    setupRequired: false,
    config: {
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-github"],
      env: [],
      type: "stdio",
      headers: {},
      url: "",
    },
    docsUrl: "",
    installed: false,
  },
];

const buildInstalledServer = (overrides?: Partial<MCPServer>): MCPServer => ({
  name: "github",
  connected: true,
  enabled: true,
  scope: ConfigScope.PROJECT,
  toolCount: 1,
  resourcesEnabled: false,
  promptsEnabled: false,
  serverInfo: {
    name: "GitHub",
    version: "1.0.0",
    description: "GitHub tools",
  },
  config: {
    command: "npx",
    args: ["-y", "@modelcontextprotocol/server-github"],
    env: [],
    type: "stdio",
    headers: {},
    url: "",
  },
  lastError: "",
  ...overrides,
});

describe("MCPSettings", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetMCPSettingsCacheForTests();
  });

  it("defaults to Discover tab when no MCP servers are installed", async () => {
    listServersMock.mockResolvedValue({ servers: [] });
    listRecommendedMock.mockResolvedValue({ recommended: baseRecommended });

    render(<MCPSettings />);

    const discoverTab = await screen.findByRole("tab", { name: /^Discover/i });

    await waitFor(() => {
      expect(discoverTab).toHaveAttribute("aria-selected", "true");
    });

    expect(screen.getByText("GitHub")).toBeInTheDocument();
  });

  it("keeps Installed tab selected when at least one MCP server is installed", async () => {
    listServersMock.mockResolvedValue({ servers: [buildInstalledServer()] });
    listRecommendedMock.mockResolvedValue({ recommended: baseRecommended });

    render(<MCPSettings />);

    const installedTab = await screen.findByRole("tab", { name: /^Installed/i });

    await waitFor(() => {
      expect(installedTab).toHaveAttribute("aria-selected", "true");
    });

    expect(screen.getByRole("button", { name: "1 tools" })).toBeInTheDocument();
  });

  it("reuses cached MCP data on repeat visits", async () => {
    listServersMock.mockResolvedValue({ servers: [buildInstalledServer()] });
    listRecommendedMock.mockResolvedValue({ recommended: baseRecommended });

    const { unmount } = render(<MCPSettings />);

    expect(
      await screen.findByRole("button", { name: "1 tools" }),
    ).toBeInTheDocument();
    unmount();

    render(<MCPSettings />);

    expect(
      screen.getByRole("button", { name: "1 tools" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(listServersMock).toHaveBeenCalledTimes(1);
    expect(listRecommendedMock).toHaveBeenCalledTimes(1);
  });
});