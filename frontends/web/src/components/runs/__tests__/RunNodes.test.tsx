import { create } from "@bufbuild/protobuf";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { RunNodeExecutionSchema } from "@/gen/reliant/v1/run_pb";
import { RunNodeList, RunNodes } from "../RunNodes";

const query = vi.hoisted(() => ({ isLoading: false, isError: false, data: undefined as unknown, refetch: vi.fn() }));
const hook = vi.hoisted(() => ({ options: undefined as any }));
vi.mock("@/hooks/run-service-hooks_gen", () => ({ useGetRun: (_req: unknown, options: unknown) => { hook.options = options; return query; } }));

describe("run node diagnostics", () => {
 it("stops polling a stopped run", () => {
  render(<RunNodes workflowId="root" />);
  expect(hook.options.refetchInterval({ state: { data: { run: { state: 3 } } } })).toBe(false);
  expect(hook.options.refetchInterval({ state: { data: { run: { state: 2 } } } })).toBe(5000);
 });
 it("shows failed command details and keeps repeated attempts separate", () => {
  render(<RunNodeList truncated nodes={[
   create(RunNodeExecutionSchema, { id: "a", nodeId: "gate", workflowId: "root", status: "failed", attempt: 1, durationMs: 25n, exitCode: 2, error: "compile failed", stdoutTail: "building", stderrTail: "token=[REDACTED]" }),
   create(RunNodeExecutionSchema, { id: "b", nodeId: "review", status: "skipped" }),
   create(RunNodeExecutionSchema, { id: "c", nodeId: "gate", status: "completed", attempt: 2 }),
  ]} />);
  expect(screen.getByText("compile failed")).toBeInTheDocument();
  expect(screen.getByText("25 ms")).toBeInTheDocument();
  expect(screen.getByText("exit 2")).toBeInTheDocument();
  expect(screen.getByText("building")).toBeInTheDocument();
  expect(screen.getByText("token=[REDACTED]")).toBeInTheDocument();
  expect(screen.getByText("skipped")).toBeInTheDocument();
  expect(screen.getAllByText("gate")).toHaveLength(2);
  expect(screen.getByText(/Older node attempts omitted/)).toBeInTheDocument();
 });
 it("has an honest empty state", () => {
  render(<RunNodeList truncated={false} nodes={[]} />);
  expect(screen.getByText("No recorded node executions yet.")).toBeInTheDocument();
 });
 // Bugs fe84af3c / eef49267: a run that failed before its first node showed
 // only "failed" and an empty node list. The reason recorded on the run is shown.
 it("says why a run failed before its first node", () => {
  query.data = { run: { state: 3, failureDetail: "workflow input validation failed: input 'asap' expects boolean, got string \"true\"" }, nodes: [], nodesTruncated: false };
  render(<RunNodes workflowId="root" />);
  expect(screen.getByRole("alert")).toHaveTextContent("Why the run failed");
  expect(screen.getByRole("alert")).toHaveTextContent("input 'asap' expects boolean");
  expect(screen.getByText("No node ran: the run failed before its first node.")).toBeInTheDocument();
  query.data = undefined;
 });
 it("surfaces a read failure instead of calling it an empty run", () => {
  query.isError = true;
  render(<RunNodes workflowId="root" />);
  expect(screen.getByRole("alert")).toHaveTextContent("Could not load node executions");
  query.isError = false;
 });
});
