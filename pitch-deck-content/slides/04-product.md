# Define a workflow, run it, get committed code

Multi-agent workflows on a React Flow canvas. Write the YAML, drag the nodes, or describe it and let Reliant build the graph.

---
**Speaker Notes:**
So this is the actual product, running today. You define a multi-agent workflow three ways: hand-write the YAML, drag nodes around this canvas, or just describe what you want and the builder generates the graph for you. Every node is an agent with its own tools and context, and when the run finishes you get committed code, not a suggestion you have to babysit.

---
**Layout Hint:**
demo-screenshot

---
**Sources:**
- React Flow node-graph canvas: web/package.json ("@xyflow/react": "^12.9.0"); node/edge components in web/src/components/workflow/nodes/ and edges/
- Drag-drop builder: web/src/components/workflow/WorkflowBuilder.tsx (onDrop/drag handlers)
- AI-generated builder: internal/workflow/builtin/build-workflow.yaml
- YAML workflow library: 20+ builtins in internal/workflow/builtin/*.yaml (agent, gsd, parallel-compete, implement-review, etc.)
- "Agentic Development Environment" framing: reliantlabs.io analysis (in-conversation)
