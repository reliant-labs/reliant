import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { ConfigScope } from "@/gen/reliant/v1/common_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { MCPSettings } from "../MCPSettings";

const listServersMock = vi.fn();
const listRecommendedMock = vi.fn();
const restartServerMock = vi.fn();
const updatePreferencesMock = vi.fn();
const loadPreferencesMock = vi.fn();

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

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
  },
}));

const baseRecommended = [
  {
    name: "github",
    displayName: "GitHub",
    description: "GitHub tools",
    category: "dev",
    config: {
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-github"],
      type: "stdio",
    },
    installed: false,
  },
];

const installedServer = {
  name: "github",
  connected: true,
  enabled: true,
  scope: ConfigScope.PROJECT,
  toolCount: 1,
  serverInfo: { name: "GitHub", version: "1.0.0" },
  config: {
    command: "npx",
    args: ["-y", "@modelcontextprotocol/server-github"],
    type: "stdio",
  },
};

let queryClient: QueryClient;

function renderSettings() {
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return render(<MCPSettings />, { wrapper });
}

describe("MCPSettings", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    // MCPService is on its generated hooks, so it is answered at the transport.
    setTransport(
      mockTransport({
        "MCPService.ListServers": (req) => listServersMock(req),
        "MCPService.ListRecommended": (req) => listRecommendedMock(req),
        "MCPService.RestartServer": (req) => restartServerMock(req),
      }),
    );
  });

  afterEach(() => setTransport(null));

  it("defaults to Discover tab when no MCP servers are installed", async () => {
    listServersMock.mockResolvedValue({ servers: [] });
    listRecommendedMock.mockResolvedValue({ recommended: baseRecommended });

    renderSettings();

    const discoverTab = await screen.findByRole("tab", { name: /^Discover/i });

    await waitFor(() => {
      expect(discoverTab).toHaveAttribute("aria-selected", "true");
    });

    expect(screen.getByText("GitHub")).toBeInTheDocument();
  });

  it("keeps Installed tab selected when at least one MCP server is installed", async () => {
    listServersMock.mockResolvedValue({ servers: [installedServer] });
    listRecommendedMock.mockResolvedValue({ recommended: baseRecommended });

    renderSettings();

    const installedTab = await screen.findByRole("tab", { name: /^Installed/i });

    await waitFor(() => {
      expect(installedTab).toHaveAttribute("aria-selected", "true");
    });

    expect(await screen.findByRole("button", { name: "1 tools" })).toBeInTheDocument();
    expect(listServersMock).toHaveBeenCalledWith(expect.objectContaining({ projectId: "project-test" }));
  });

  it("reuses cached MCP data on repeat visits", async () => {
    listServersMock.mockResolvedValue({ servers: [installedServer] });
    listRecommendedMock.mockResolvedValue({ recommended: baseRecommended });

    const { unmount } = renderSettings();

    expect(
      await screen.findByRole("button", { name: "1 tools" }),
    ).toBeInTheDocument();
    unmount();

    renderSettings();

    expect(
      screen.getByRole("button", { name: "1 tools" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(listServersMock).toHaveBeenCalledTimes(1);
    expect(listRecommendedMock).toHaveBeenCalledTimes(1);
  });

  it("refetches the server list after a restart, so the status shown is the server's", async () => {
    let connected = false;
    listServersMock.mockImplementation(async () => ({ servers: [{ ...installedServer, connected }] }));
    listRecommendedMock.mockResolvedValue({ recommended: baseRecommended });
    restartServerMock.mockImplementation(async () => {
      connected = true;
      return { success: true, name: "github" };
    });

    renderSettings();
    await screen.findByRole("button", { name: "1 tools" });
    expect(listServersMock).toHaveBeenCalledTimes(1);

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /^Server settings for/ }));
    await user.click(screen.getByRole("button", { name: /^Restart/ }));

    await waitFor(() => expect(restartServerMock).toHaveBeenCalledWith(expect.objectContaining({ projectId: "project-test", name: "github" })));
    await waitFor(() => expect(listServersMock).toHaveBeenCalledTimes(2));
  });
});
