// Copyright (c) 2026 Reliant Labs

import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";

import { RaiseCoverageButton } from "./TestsAgentActions";

const { ask, error } = vi.hoisted(() => ({ ask: vi.fn(), error: vi.fn() }));

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "" }));
vi.mock("@/lib/transportScope", () => ({ transportOf: () => undefined }));
vi.mock("@/lib/orgScope", () => ({
  useOrgScope: () => ({ ready: true, orgId: "org" }),
  whenOrgScopeReady: vi.fn(),
}));
vi.mock("@/hooks/project-queries", () => ({ useProject: () => ({ is_git_repo: true }) }));
vi.mock("@/lib/toast-manager", () => ({ toast: { error } }));
vi.mock("../Observability/AskReliant/askAgentMode", () => ({ useAskAgentMode: () => ({ mode: "chat" }) }));
vi.mock("../Observability/AskReliant/AskReliant", () => ({
  ASK_AGENT_HINT: "Ask an agent",
  ASK_AGENT_LABEL: "Ask an agent",
  useAskReliant: () => ({ ask, busy: false, dialog: null, mode: "chat" }),
}));

it("refuses the coverage agent action as Unimplemented without a control plane or transport scope", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const fetchQuery = vi.spyOn(client, "fetchQuery");
  const view = render(
    <QueryClientProvider client={client}>
      <RaiseCoverageButton project="app" projectId="project" pkg="internal/example" target={{ gitCommit: "abc" }} currentPercent={50} />
    </QueryClientProvider>
  );

  fireEvent.click(screen.getByTestId("tests-coverage-raise-button"));

  await waitFor(() => expect(error).toHaveBeenCalledWith(expect.any(ConnectError)));
  expect(error.mock.calls[0][0].code).toBe(Code.Unimplemented);
  expect(fetchQuery).not.toHaveBeenCalled();
  expect(ask).not.toHaveBeenCalled();

  view.unmount();
  client.clear();
});
