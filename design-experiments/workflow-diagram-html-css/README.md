# Reliant Workflow Value Diagram

## Concept

This standalone design experiment presents a Reliant-style workflow as an adaptive production pipeline. The goal is to make the value of workflows visible at a glance: one chat request expands into a planned, parallel, reviewed, retryable system rather than a single linear assistant response.

The composition uses a large poster-like canvas with glass panels, neon routing lines, and a content-rich diagram. The top section frames the promise: one chat becomes planning, parallel agents, retries, rethink, and final synthesis. The main diagram then shows the operational path from request to final answer, including the failure loop.

## Flow

1. **One chat request** starts the workflow.
2. **Agent A plans the route** by defining scope, dependencies, and validation.
3. **Parallelizable work packets fan out** from the plan.
4. **Agent B, Agent C, and Agent D** run separate research, build, and test loops.
5. **Structured review** gates the work with pass/fail outcomes.
6. **Failing work retries with feedback** up to three attempts.
7. **If retries exceed 3**, the diagram routes back to rethinking the plan.
8. **Final response** receives only reviewed work.

## Visual Design

- **Colors:** Deep navy and black create a high-contrast command-center backdrop. Cyan represents planning and orchestration, violet/magenta represent parallel agent activity, amber represents review and decision points, green represents successful final output, and red marks retry/failure paths.
- **Layout:** The desktop layout uses a five-column grid so the pipeline reads left to right while the retry and rethink paths sit below the main flow. This keeps the happy path clear while still showing capability depth.
- **Depth:** Frosted glass panels, radial glows, grid texture, and luminous connectors give the diagram a polished systems-map feel without relying on images or JavaScript.
- **Animation:** CSS-only loop indicators move inside each agent card to imply ongoing custom loops. A `prefers-reduced-motion` media query disables the motion for users who request reduced animation.
- **Responsiveness:** At narrower widths, the diagram collapses into a stacked presentation and hides decorative connector lines so the content remains readable.

## How to View

Open `index.html` directly in a browser, or serve this directory with any static file server:

```bash
cd design-experiments/workflow-diagram-html-css
python3 -m http.server 8080
```

Then visit `http://localhost:8080`.

## Files

- `index.html` contains the semantic HTML structure for the diagram.
- `styles.css` contains all visual design, layout, and animation rules.
