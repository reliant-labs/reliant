// @vitest-environment node
import { describe, it, expect } from "vitest";
import * as yaml from "js-yaml";
import { serializeWorkflowToYAML, deserializeWorkflowFromYAML } from "../workflow-serializer";
import type { Workflow } from "../../types/workflow";

/**
 * The node base keys every node kind may carry, and the workflow's observe
 * defaults, survive a load and a save: daemon, outcome, tolerate_failure and
 * observe were dropped before, so a definition opened in the builder lost them.
 */

const definition = `
name: rc-train
observe:
  metrics:
    - name: node.duration
      kind: histogram
      unit: ms
      value: "{{ node.duration_ms }}"
      attributes:
        node_type: "{{ node.type }}"
nodes:
  - id: land
    type: run
    command: ./land.sh
    daemon:
      pool: gate
    tolerate_failure: true
    observe:
      attributes:
        exit: "{{ output.exit_code }}"
      events:
        - name: decision
          when: output.exit_code == 0
          each: "{{ output.stdout.split('\\\\n') }}"
          attributes:
            name: land
            value: "{{ item.split(' ')[2] }}"
      metrics:
        - name: github.api.calls
          kind: counter
          unit: "{call}"
          value: "{{ 14 }}"
          attributes:
            identity: bot
  - id: fail
    type: run
    command: exit 1
    daemon: cloud
    outcome: failure
  - id: named
    type: run
    command: "true"
    daemon: my-mac
    preflight: true
  - id: checked
    type: run
    command: "true"
    preflight:
      tools: [go, task]
      min_free_disk: 10GiB
      max_load: 2.5
      allow_root: true
      max_machines: 2
  - id: dynamic
    type: run
    command: "true"
    daemon: "{{ inputs.machine }}"
  - id: labelled
    type: run
    command: "true"
    daemon:
      labels:
        gpu: "true"
entry:
  - land
`;

function roundTrip(source: string): Record<string, any> {
  return yaml.load(serializeWorkflowToYAML(deserializeWorkflowFromYAML(source))) as Record<string, any>;
}

describe("workflow serializer: node base keys", () => {
  const original = yaml.load(definition) as Record<string, any>;
  const saved = roundTrip(definition);
  const node = (id: string) => saved.nodes.find((n: { id: string }) => n.id === id);
  const originalNode = (id: string) => original.nodes.find((n: { id: string }) => n.id === id);

  it("keeps observe on a node and on the workflow", () => {
    expect(node("land").observe).toEqual(originalNode("land").observe);
    expect(saved.observe).toEqual(original.observe);
  });

  it("keeps tolerate_failure and outcome", () => {
    expect(node("land").tolerate_failure).toBe(true);
    expect(node("fail").outcome).toBe("failure");
    expect(node("fail")).not.toHaveProperty("tolerate_failure");
    expect(node("land")).not.toHaveProperty("outcome");
  });

  it("keeps preflight, true or its overrides", () => {
    expect(node("named").preflight).toBe(true);
    expect(node("checked").preflight).toEqual(originalNode("checked").preflight);
    expect(node("land")).not.toHaveProperty("preflight");
  });

  it("keeps every daemon form", () => {
    expect(node("land").daemon).toEqual({ pool: "gate" });
    expect(node("fail").daemon).toBe("cloud");
    expect(node("named").daemon).toBe("my-mac");
    expect(node("dynamic").daemon).toBe("{{ inputs.machine }}");
    expect(node("labelled").daemon).toEqual({ labels: { gpu: "true" } });
  });

  it.each(["", "{{ inputs.machine }}", "local", " "])("preserves the daemon expression scalar %j", (expression) => {
    const workflow: Workflow = {
      name: "expression",
      nodes: [{ id: "run", type: "run", daemon: { value: { case: "expr", value: expression } } }],
    };
    const written = yaml.load(serializeWorkflowToYAML(workflow)) as Record<string, any>;
    expect(written.nodes[0]).toHaveProperty("daemon", expression);
  });

  it("is stable: a second save writes what the first did", () => {
    const again = yaml.load(serializeWorkflowToYAML(deserializeWorkflowFromYAML(yaml.dump(saved))));
    expect(again).toEqual(saved);
  });

  it("writes a node built in the builder", () => {
    const workflow: Workflow = {
      name: "built",
      nodes: [
        {
          id: "review",
          type: "call_llm",
          tolerateFailure: true,
          outcome: "success",
          daemon: { value: { case: "literal", value: { type: "local" } } },
          observe: {
            events: [{ name: "decision", attributes: { name: "review_verdict", value: "{{ output.label }}" } }],
          },
        },
      ],
      entry: ["review"],
    };
    const written = yaml.load(serializeWorkflowToYAML(workflow)) as Record<string, any>;
    expect(written.nodes[0]).toMatchObject({
      id: "review",
      daemon: "local",
      outcome: "success",
      tolerate_failure: true,
      observe: { events: [{ name: "decision", attributes: { name: "review_verdict", value: "{{ output.label }}" } }] },
    });
    expect(written).not.toHaveProperty("observe");
  });

  it("writes nothing for a node that sets none of them", () => {
    const written = roundTrip("name: bare\nnodes:\n  - id: a\n    type: run\n    command: 'true'\n");
    for (const key of ["daemon", "outcome", "tolerate_failure", "observe", "preflight"]) {
      expect(written.nodes[0]).not.toHaveProperty(key);
    }
  });
});
