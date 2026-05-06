# Workflow Diagram React Experiment

This standalone React experiment shows how one Reliant chat can become an adaptive execution pipeline. It improves the existing static React version in place by keeping the same planning, parallel work, review, retry, rethink, and final answer story while moving the primary diagram rendering to React Flow.

## What Changed

- Replaced the static layout rails with a React Flow canvas powered by `@xyflow/react`.
- Added custom node types for the planner, parallel fan-out, agent loops, structured review hub, retry path, rethink path, and final answer.
- Added labeled directional edges so pass, fail, retry, targeted feedback, and rethink loops read as actual workflow connections.
- Strengthened the visual hierarchy with a central decision hub, clearer branch colors, dashed recovery paths, and a polished control-room presentation.
- Added a local SVG favicon and refreshed the validation screenshot.

## React Flow Structure

The diagram is defined in `src/main.jsx` with declarative `nodes` and `edges` arrays.

### Nodes

- `planner`: Agent A decomposes the chat into scope, dependencies, acceptance criteria, and parallel lanes.
- `fanout`: Converts the plan into parallelizable work packets.
- `agent`: Three specialized agents run implementation, research, and test loops with evidence outputs.
- `review`: The structured review decision hub returns pass or fail with reasons.
- `retry`: The fail path applies reviewer feedback through three visible attempts.
- `rethink`: The recovery path triggers when retries exceed three and loops back to planning.
- `final`: The pass path synthesizes verified outputs, review rationale, risks, and next actions.

### Edges

- `plan-to-fanout`: Agent A emits parallelizable work.
- `fanout-to-agent-*`: Work launches into parallel agents.
- `agent-*-to-review`: Agents submit evidence to structured review.
- `review-to-final`: The pass branch reaches final synthesis.
- `review-to-retry`: The fail branch returns reasons to the retry path.
- `retry-to-agent-b`: Targeted feedback loops back to a failing branch.
- `retry-to-review`: Corrected output resubmits to review.
- `retry-to-rethink` and `rethink-to-plan`: Exceeding three retries changes the plan and relaunches the pipeline.

## Visual System

The experiment keeps the original dark control-room direction and makes the workflow easier to scan:

- Cyan and blue represent planning, fan-out, and normal work flow.
- Violet and amber distinguish specialized parallel agent lanes.
- Green marks the successful pass path and final answer.
- Rose marks review failure and targeted retry feedback.
- Dashed amber marks the exceptional rethink path back to Agent A.

## How to View

```bash
cd design-experiments/workflow-diagram-react
npm install
npm run dev
```

Open the local Vite URL printed in the terminal. For the validation run, the preview was served at:

```text
http://127.0.0.1:4179/?v=3
```

To create a production build, run:

```bash
npm run build
npm run preview
```

## Validation Results

Validation was run against the standalone experiment without editing app or onboarding files.

### Build

Command:

```bash
cd design-experiments/workflow-diagram-react && npm run build
```

Result: passed.

Notable output:

```text
✓ 194 modules transformed.
dist/index.html                   0.70 kB │ gzip:   0.42 kB
dist/assets/index-DXOdaMyh.css   28.56 kB │ gzip:   5.96 kB
dist/assets/index-DWWmzCxt.js   603.77 kB │ gzip: 181.33 kB
✓ built in 842ms
```

Vite also reported a chunk-size warning because the React Flow bundle pushes the JavaScript chunk above 500 kB after minification.

### Browser Screenshot

The project was served locally and inspected in Chrome DevTools at `http://127.0.0.1:4179/?v=3`.

- Screenshot saved to `validation-screenshot.png`.
- Browser console check found no warnings or errors after adding the local favicon.
- Visual inspection confirmed the React Flow diagram renders with the planner, fan-out, three parallel agent loops, structured review hub, fail/retry loop, rethink loop, and final answer visible in one coherent frame.

### Forge UI Audit

Command:

```bash
forge ui-audit http://127.0.0.1:4179/?v=3 --profile diagram --scope '[data-audit="workflow-diagram-react-flow"]' --out /Users/user/.reliant/worktrees/00e933a0b4ee/feat/nw-wf/design-experiments/workflow-diagram-react/ui-audit --wait-for '.react-flow__node' --timeout 30000
```

Result: passed with warnings and wrote artifacts to `ui-audit/report.json`, `ui-audit/index.html`, viewport screenshots, annotated screenshots, and DOM JSON files.

Output:

```text
UI Audit: diagram
URL: http://127.0.0.1:4179/?v=3

✓ desktop 1440x900
  ! max-text-lines: scope uses 64 rendered text lines
  ! inside-viewport: scope extends outside the initial viewport
✓ tablet 1024x768
  ! max-text-lines: scope uses 64 rendered text lines
  ! inside-viewport: scope extends outside the initial viewport
✓ mobile 390x844
  ! max-text-lines: scope uses 64 rendered text lines
  ! inside-viewport: scope extends outside the initial viewport
```

The warnings are expected for this comparison artifact because the diagram is intentionally content-dense and the standalone page scrolls vertically rather than compressing the full workflow into the initial viewport.