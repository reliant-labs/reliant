import { create } from "@bufbuild/protobuf";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { RunNodeExecutionSchema } from "@/gen/reliant/v1/run_pb";
import { NodeObservationSchema, ObservedEventSchema } from "@/gen/reliant/v1/common_pb";
import { RunNodeList, RunNodes, decisionLine, runEntries } from "../RunNodes";

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
 // What a node recorded with observe: is shown under the attempt it belongs
 // to: a decision as one line, the rest behind "Observed". A node the
 // workflow completed itself has no attempt row and gets an entry of its own.
 it("shows what each node recorded, under its attempt or on its own", () => {
  const decision = (value: string, reason: string) => create(ObservedEventSchema, { name: "decision", attributes: { name: "cut", value, reason } });
  render(<RunNodeList truncated={false} observationsTruncated nodes={[
   create(RunNodeExecutionSchema, { id: "a", nodeId: "world", nodePath: "world", workflowId: "root", status: "completed", attempt: 1, completedAt: "2026-11-24T12:00:01Z" }),
  ]} observations={[
   create(NodeObservationSchema, { nodeId: "world", nodePath: "world", workflowId: "root", attempt: 1, createdAt: "2026-11-24T12:00:01Z",
    attributes: { behind: 0 }, events: [create(ObservedEventSchema, { name: "tick", attributes: { lane: "fast" } })], errors: ["events[1].attributes.x: no such key"] }),
   create(NodeObservationSchema, { nodeId: "plan", nodePath: "plan", workflowId: "root", attempt: 1, createdAt: "2026-11-24T12:00:02Z",
    events: [decision("no_cut", "main_unchanged")] }),
  ]} />);
  expect(screen.getByText("cut: no_cut (main_unchanged)")).toBeInTheDocument();
  expect(screen.getByText("plan")).toBeInTheDocument();
  expect(screen.getByText("recorded")).toBeInTheDocument();
  expect(screen.getByText("tick")).toBeInTheDocument();
  expect(screen.getByText("lane")).toBeInTheDocument();
  expect(screen.getByText(/Expression failed \(the node was unaffected\): events\[1\]/)).toBeInTheDocument();
  expect(screen.getByText(/Older observations omitted/)).toBeInTheDocument();
 });
 it("joins an observation to its attempt by workflow, node, loop scope and attempt, newest first", () => {
  const nodes = [
   create(RunNodeExecutionSchema, { id: "b", nodeId: "land", nodePath: "sweep.land", workflowId: "root", loopNodeId: "sweep", loopIteration: 1, attempt: 2, completedAt: "2026-11-24T12:00:05Z" }),
   create(RunNodeExecutionSchema, { id: "a", nodeId: "land", nodePath: "sweep.land", workflowId: "root", loopNodeId: "sweep", loopIteration: 0, attempt: 1, completedAt: "2026-11-24T12:00:03Z" }),
  ];
  const obs = (iteration: number, attempt: number, createdAt: string) => create(NodeObservationSchema, {
   nodeId: "land", nodePath: "sweep.land", workflowId: "root", loopNodeId: "sweep", loopIteration: iteration, attempt, createdAt });
  const entries = runEntries(nodes, [obs(1, 2, "2026-11-24T12:00:05Z"), obs(0, 1, "2026-11-24T12:00:03Z"), obs(0, 3, "2026-11-24T12:00:04Z")]);
  expect(entries.map((e) => e.kind)).toEqual(["node", "observation", "node"]);
  expect(entries[0].kind === "node" && entries[0].observations).toHaveLength(1);
  expect(entries[2].kind === "node" && entries[2].observations).toHaveLength(1);
 });
 it("reads a decision with or without its name and reason", () => {
  expect(decisionLine(create(ObservedEventSchema, { name: "decision", attributes: { value: "pass" } }))).toBe("pass");
  expect(decisionLine(create(ObservedEventSchema, { name: "tick", attributes: { value: "pass" } }))).toBe("");
 });
});
