# Workflow Diagram SVG Experiment

## Concept

This experiment presents Reliant-style workflows as a high-density execution graph: a single chat expands into an adaptive pipeline with explicit planning, parallelizable outputs, parallel custom agent loops, structured review, bounded retries, rethink paths, and a final answer. The visual direction is a polished systems-control-board diagram rather than a simple flowchart, so the diagram can communicate both clarity and capability depth at a glance.

## Flow

1. One chat enters with user intent, files, and constraints.
2. Agent A plans the work, defining ordered tasks, checks, and exit criteria.
3. The plan emits a work graph of parallelizable outputs with dependency awareness.
4. Multiple agents run custom loops in parallel: implementation, validation, and docs or UX polish.
5. A structured review gate evaluates the parallel outputs against a rubric and returns pass or fail.
6. Passing work moves to the final answer with traceability.
7. Failed work retries with targeted feedback into the agent loops.
8. If retries exceed three attempts, the path returns to rethink the plan, scope, or dependencies before continuing.

## Visual Design

The diagram uses a deep navy control-room background with subtle grid lines and cyan/violet glow fields to suggest a multi-agent orchestration surface. Cards use translucent dark panels, rounded geometry, and bright accent gradients to separate roles: cyan for primary flow, teal and blue for parallel agent loops, amber for structured review and feedback, green for passing work, and rose for the rethink path.

The composition emphasizes left-to-right progress while preserving adaptive loops. Animated dashed connectors make flow direction visible without requiring interaction. The animation is implemented inside the SVG with CSS and respects `prefers-reduced-motion` by disabling motion when requested.

## Files

- `workflow-diagram.svg` is the primary SVG-only artifact.
- `index.html` is a minimal standalone local viewer that embeds the SVG as an image.

## How to View

Open `index.html` in any modern browser, or serve the directory with a static server and visit the local URL:

```bash
cd design-experiments/workflow-diagram-svg
python3 -m http.server 4177
```

Then open `http://localhost:4177`.
