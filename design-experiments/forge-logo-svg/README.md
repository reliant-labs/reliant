# Forge Logo SVG Experiment

## Concept

This experiment positions Forge as an enabling structure for LLMs: not a user-facing chat surface, but a crutch, harness, and production scaffold that helps an agent create production-ready applications from day 0. The revised mark replaces the literal `F` scaffold with an anvil made from mesh material. It should read as forge/anvil first, then reveal AI infrastructure through graph nodes, blueprint lines, and support struts.

## Geometry

- The primary silhouette is an abstract anvil: a horn, face, waist, and base reduced to simple engineered planes.
- The anvil is constructed from graph-node joints and mesh edges so the material feels computational rather than blacksmith-literal.
- Blueprint grid lines and corner ticks keep the icon aligned to repeatable app-generation constraints.
- Blue harness struts connect into the anvil, suggesting the product stabilizes and guides LLM builders rather than asking users to operate the model directly.
- The warm ember line stays at the base as a restrained production-readiness signal, not a decorative flame.

## Colors

- `#070B12` and `#111A27` create a serious developer-tool field with enough contrast for icon use.
- `#E8EEF8` and `#AAB8CC` make the nodes read as steel: precise, durable, and production-oriented.
- `#5A9CFF` is the blueprint, harness, and mesh color, mapping to reliability, automation, and structure.
- `#FF6A3D` is restrained forge heat, used only as a foundation readiness cue.

## Data Audit Tags

- `data-audit="forge-logo-svg"` wraps the primary preview surface in `index.html`.
- `data-audit="primary-logo-surface"` and `data-audit="primary-svg-artifact"` identify the main rendered logo.
- `data-audit="scale-previews"`, `data-audit="scale-preview-72"`, and `data-audit="scale-preview-48"` identify the small-size checks.
- `data-audit="concept-notes"`, `data-audit="experiment-label"`, `data-audit="concept-summary"`, and `data-audit="color-palette"` identify meaningful documentation sections.
- Inside `forge-mark.svg`, audit tags mark `blueprint-field`, `precision-frame`, `mesh-anvil`, `production-nodes`, and `harness-readiness`.

## Mapping to Forge

Forge is represented as the support system around the builder and inside the material itself. The anvil is not a nostalgic blacksmith symbol; it is a production base made from the same graph-like structure an AI system would use to plan, connect, and verify software. That maps to the product brief: a crutch and harness for LLMs that scaffolds real apps with production discipline from the first generated artifact.

## View Locally

Open `index.html` in a browser, or serve the directory with any static file server:

```bash
cd design-experiments/forge-logo-svg
python3 -m http.server 4173
```

Then visit `http://localhost:4173`.

You can also open the primary artifact directly at `forge-mark.svg` or `http://localhost:4173/forge-mark.svg` when the static server is running.

## Validation

- XML parsing: `xmllint --noout forge-mark.svg` passed.
- XML fallback parsing: Python `xml.etree.ElementTree` parsing passed.
- Preview rendering: Chrome MCP loaded `http://127.0.0.1:4173/index.html?v=2` with no console errors; the primary SVG reported `512 x 512` natural dimensions.
- Direct SVG rendering: Chrome MCP loaded `http://127.0.0.1:4173/forge-mark.svg?v=2`; the document exposed the SVG root and 15 production-node circles.
- Screenshot inspection: Chrome MCP captured the actual browser preview to `validation-screenshot.png`.

## Files

- `forge-mark.svg` is the primary SVG-only artifact.
- `index.html` is a standalone local preview page using the same SVG.
- `README.md` documents the design rationale, audit tags, viewing instructions, and validation status.