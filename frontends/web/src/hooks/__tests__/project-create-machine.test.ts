import { afterEach, expect, it, vi } from "vitest";
import { createProjectWithResult } from "../project-queries";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";
import { queryClient } from "@/lib/query-client";
const success = vi.hoisted(() => vi.fn());
vi.mock("@/lib/toast-manager", () => ({ toast: { success, error: vi.fn() } }));
afterEach(() => { setTransport(null); queryClient.clear(); });
it("sends the selected daemon and distinguishes attaching an existing project", async () => {
  const create = vi.fn(async (_request: unknown) => ({ project: { id: "p", name: "API" }, attached: true, projectDaemon: { projectId: "p", daemonId: "remote", daemonName: "Server", path: "/repo" } }));
  setTransport(mockTransport(serviceHandlers("ProjectService", { createProject: create })));
  const result = await createProjectWithResult({ name: "repo", path: "/repo", daemon_id: "remote" });
  expect(create.mock.calls[0][0]).toMatchObject({ daemonId: "remote", path: "/repo" });
  expect(result).toMatchObject({ attached: true, project: { id: "p" }, projectDaemon: { daemon_id: "remote", daemon_name: "Server" } });
  expect(success).toHaveBeenCalledWith("Added Server to existing project API");
});
