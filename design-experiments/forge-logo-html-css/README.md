# Forge Logo HTML/CSS Experiment

A standalone HTML + CSS-only logo concept for Forge, built as a serious developer-tool mark that can work as an app icon, product logo, or small-size symbol without SVG, canvas, or JavaScript.

## What Changed

This pass replaces the earlier literal `F` scaffold with an AI/futuristic anvil made from mesh material. The mark now reads as a hardened graph structure: nodes, struts, blueprint guides, and hot harness rails combine into an anvil silhouette that still communicates support, scaffolding, and production readiness.

- Rebuilt the primary mark as a graph-node anvil rather than an `F`-scaffold.
- Added mesh struts and circular node joints to make the anvil feel like AI infrastructure material.
- Kept the orange forge heat as a load-bearing harness rail that braces the structure.
- Preserved blueprint framing and grid context so the logo still feels planned, scaffolded, and day-zero production oriented.
- Tuned app-icon, standalone, and small-size specimens from the same HTML/CSS construction.

## Concept

Forge is a crutch and harness for LLMs to use, not a tool for users to use LLMs. This mark treats Forge as the production surface agents build against: an anvil that is also a mesh support system. The graph nodes imply model-era coordination and tool routing, while the anvil silhouette communicates making, shaping, hardening, and readiness.

The identity is intentionally more infrastructure than spark. It avoids generic chat or magic motifs and instead emphasizes load paths, scaffolding, blueprint discipline, and a serious developer-tool foundation.

## Geometry

- The symbol is drawn from a 100-unit square through CSS custom properties, so one construction scales across the hero icon and specimen cards.
- `.anvil-mass` defines the clipped anvil silhouette with a left heel, central waist, right horn, and broad production base.
- `.mesh-line` elements create the graph-material struts inside and around the anvil body.
- `.mesh-node` elements mark connection points; `.signal-node` highlights active hot nodes in the harness path.
- `.harness-rail` elements form the orange support rails that visually brace the anvil, mapping Forge to a harness for agents.
- `.blueprint-frame` and `.blueprint-axis` elements provide planning and scaffold context behind the mark.

## Data Audit Tags

- `data-audit="forge-logo-html-css-page"` scopes the full page shell.
- `data-audit="forge-logo-html-css-hero"` scopes the main hero section.
- `data-audit="forge-logo-html-css-showcase"` scopes the primary logo plus wordmark story.
- `data-audit="forge-logo-html-css"` identifies the primary logo surface used for UI audit.
- `data-audit="mesh-anvil-primary"` identifies the primary HTML/CSS mark inside the logo surface.
- `data-audit="forge-logo-html-css-story"` scopes the concept copy.
- `data-audit="forge-logo-html-css-specimens"` scopes the specimen grid.
- `data-audit="specimen-app-icon"`, `data-audit="specimen-standalone-mark"`, and `data-audit="specimen-scale-test"` identify the supporting previews.

## Colors

- Carbon black: `#070a0f` for the developer-tool foundation.
- Deep panel blues: `#0c141d`, `#121f2b`, and `#132331` for the icon field and presentation surfaces.
- Steel mesh: `#fbfdff`, `#b8cad9`, and `#5c7182` for durable graph material.
- Forge heat: `#ff6f2c`, `#ffc071`, and `#d94b17` for active harness rails and signal nodes.
- Blueprint blue: `#68b7ff` and `#8fd0ff` for grid, frame, and AI-infrastructure glow.

## View Locally

Serve the folder with any static server:

```bash
cd design-experiments/forge-logo-html-css
python3 -m http.server 4173 --bind 127.0.0.1
```

Then visit `http://127.0.0.1:4173/`.

## Validation

Chrome MCP inspection was run against `http://127.0.0.1:4173/?mesh=2`. The accessibility snapshot found the primary image label, headline, story copy, and all three specimen labels. A full-page screenshot was saved to `data/forge-logo-html-css-mesh-final.png`, and a focused screenshot of the primary logo surface was inspected in Chrome.

Console validation reported no warnings or errors.

Source verification command:

```bash
grep -R "<script\|<svg\|<canvas\|<iframe\|<object\|<embed\|javascript:" -n design-experiments/forge-logo-html-css/index.html design-experiments/forge-logo-html-css/styles.css || true
```

Output: no matches. The primary logo is built from HTML elements and CSS only; `index.html` contains semantic containers and spans, while `styles.css` defines the geometry, gradients, clipping, and presentation.

UI audit command:

```bash
forge ui-audit http://127.0.0.1:4173/?mesh=2 --profile generic --scope '[data-audit="forge-logo-html-css"]' --out /Users/user/.reliant/worktrees/00e933a0b4ee/feat/nw-wf/design-experiments/forge-logo-html-css/ui-audit --wait-for '[data-audit="forge-logo-html-css"]' --timeout 30000
```

Result: passed for desktop `1440x900`, tablet `1024x768`, and mobile `390x844` with no findings. Artifacts were written to `design-experiments/forge-logo-html-css/ui-audit/report.json` and `design-experiments/forge-logo-html-css/ui-audit/index.html`.
