---
name: diagrams
description: Drawing architecture, flow, and relationship diagrams without coordinate drift — reach for the component library first, declare a shared coordinate space when hand-rolling SVG+DOM, and explore multiple layouts for fan-out/anywhere topologies before committing.
emit: both
---

# Diagrams

LLMs are bad at diagrams. The specific failure modes:

- **Coordinate drift** — connector lines computed in SVG coordinates that don't match the absolute-positioned DOM nodes they're supposed to point at.
- **Overlap and crowding** — boxes that intersect, labels that escape their parent, connectors that cross unnecessarily.
- **Wrong layout for the relationship** — drawing a "daemon connects to N places" idea as a single linear chain (which says "one place") because chains are the easy default.
- **Slop SVGs** — multi-path icons for "server", "cloud", "database" hand-drawn from scratch when a `lucide-react` icon would be correct and consistent.

This skill exists to prevent those.

**Also load `frontend/design`.** It carries the visual brief, the verify-visually loop, and a copy-pasteable overflow probe you will want when checking your diagram. Diagrams are exactly the case both skills are tuned for — load them together, not just this one.

## Reach for a component library before hand-rolling

If your environment ships a diagram component library, search it first. A well-built diagram primitive handles edge routing, anchor math, and consistent visual weight — none of which an LLM gets right on the first try from a blank file. If a library component is 80% right, install it and adapt; hand-rolling from scratch should be your last move.

<!-- @forge-only:start -->
### Forge's component library

Forge ships a component library with diagram primitives. Search it:

```
component_library(action="search", category="diagrams")
component_library(action="get", name="architecture_diagram")
```

If you don't have the `component_library` tool in your environment, read the component files directly from disk: they live under `<forge-repo>/components/components/diagrams/<name>.tsx` (or `layouts/`, `charts/`, etc. by category). The registry in `<forge-repo>/components/library.go` lists every component, its category, and tags.

Shipped diagram components and what they're for:

| Component | Use for |
|-----------|---------|
| `flow_horizontal` | Linear sequence of steps with optional loop-back. Status per step (completed/active/pending). |
| `process_steps` | Numbered process with descriptions per step. |
| `architecture_diagram` | Grouped services with arbitrary connections between them. Handles edge routing. |
| `org_chart` | Hierarchical tree. |
| `bus_bar` | Pub/sub bar — producers on one side, consumers on the other, labeled bus in the middle. Communicates decoupling (Kafka/NATS/EventBridge). |
| `pub_sub_matrix` | Topics × consumers grid, cells marking subscriptions. Pairs with `bus_bar` to make routing rules scannable. |
| `variation_grid` | Lay out 2-4 candidate diagrams (or any designs) as labeled artboards for the user to compare. Use whenever you produce alternative layouts for the same topology. |

Narrative charts (`quadrant_chart`, `funnel_chart`, `concentric_circles`) handle all coordinate math internally — pass data, get pixels. Same applies to the diagram components above: they own positioning and edge routing. For commodity data viz (bar, line, area, donut, scatter) use Recharts — see the `frontend` skill.
<!-- @forge-only:end -->

## When you must hand-roll: declare a shared coordinate space

The single mistake to never make: drawing connector lines in SVG coordinates while positioning the boxes in CSS pixels and hoping the two systems agree. They won't, and the resulting drift is visible.

**The rule:** define your geometry ONCE — width, height, named anchor points — and use those same numbers for both the SVG `viewBox` AND the absolute-positioned overlay nodes.

```tsx
function FanOutDiagram() {
  // single coordinate space (px) shared by the SVG viewBox and the cards
  const W = 1060, H = 432;
  const rows    = [36, 121, 206, 291, 376];   // target card vertical centers
  const dstL    = 556, dstR = 762;             // target card left / right edges
  const hubR    = { x: 422, y: 206 };          // hub card right edge
  const sinkL   = { x: 884, y: 206 };          // sink card left edge

  const curve = (x1, y1, x2, y2) => {
    const mx = (x1 + x2) / 2;
    return `M${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`;
  };

  return (
    <div style={{ position: "relative", width: W, height: H }}>
      <svg viewBox={`0 0 ${W} ${H}`} style={{ position: "absolute", inset: 0, width: "100%", height: "100%" }}>
        {rows.map((y, i) => (
          <g key={i}>
            <path d={curve(hubR.x, hubR.y, dstL, y)} stroke="currentColor" fill="none" />
            <path d={curve(dstR, y, sinkL.x, sinkL.y)} stroke="currentColor" fill="none" />
          </g>
        ))}
      </svg>

      {/* hub */}
      <Node style={{ position: "absolute", left: 224, top: hubR.y, transform: "translateY(-50%)" }} />
      {/* targets */}
      {targets.map((t, i) => (
        <Node key={t.id} style={{ position: "absolute", left: dstL, top: rows[i], transform: "translateY(-50%)" }} />
      ))}
      {/* sink */}
      <Node style={{ position: "absolute", left: sinkL.x, top: sinkL.y, transform: "translateY(-50%)" }} />
    </div>
  );
}
```

The container width is `W` exactly, the SVG `viewBox` is `0 0 W H`, and every card's `left` / `top` comes from the same named constants the SVG paths use. The lines land on the cards because they're computed against the same numbers.

If you later need the layout to scale responsively, give the container `width: W` plus an outer wrapper that scales it with `transform: scale(...)` — don't try to make `W` flexible and chase the connector math.

## Explore multiple layouts for 1-to-many and many-to-many topologies

When the user asks for a diagram showing "one thing connects to many things" — daemons running anywhere, services routing to N regions, a hub-and-spoke — the default LLM output is a single linear chain. That undersells the relationship.

This applies just as strongly to **many-to-many pub/sub buses** (Kafka, NATS, EventBridge, Redis pub/sub, message queues with multiple producers and consumers). For 1-to-many, many-to-many, fan-out, hub-and-spoke, or any topology where N producers talk to M consumers, produce 2-3 layouts. A horizontal bus bar, a hub-and-spoke, and a matrix all describe the same data and each implies something different about which dimension the reader should focus on — pub/sub is arguably MORE in need of variation exploration than simple fan-out because the topology has multiple valid visual encodings.

For these topologies, default to producing **2-3 layouts the user can compare**:

- **Stack** — destinations listed vertically, hub on the left. Reads like a menu.
- **Wizard / pick-one** — hub-and-one-destination, plus a selector that swaps the destination. Communicates choice.
- **Fan-out** — hub centered, destinations arrayed with explicit connector lines. Communicates breadth.
- **Bus bar** — a horizontal/vertical bar with producers on one side and consumers on the other. Communicates decoupling (pub/sub).
- **Matrix** — producers as rows, consumers as columns, cells marking who subscribes to what. Communicates routing rules.

Each says something different about the relationship. Render them in a labeled grid (one `<figure>` per variant with a `<figcaption>`) so the user can scan them side by side. Don't quietly commit to chains.

<!-- @forge-only:start -->
In forge, use `component_library(action="get", name="variation_grid")` to render the side-by-side artboard layout — it handles consistent spacing across 2-4 variants.
<!-- @forge-only:end -->

**If you are writing static HTML (no React env), hand-roll the artboard layout:** a grid of labeled `<figure>` elements with a `<figcaption>` per variant, each holding one rendering of the topology. The discipline is "show 2-3 layouts side by side," not "use any specific component."

## Icons: use a library, don't draw them

Use `lucide-react` (or your stack's equivalent icon library) for icons inside diagram nodes (`Cloud`, `Server`, `Database`, `Monitor`, `FolderCode`, `Cpu`, etc.). One icon family, sized consistently (16-22px is typical for diagram-node icons).

Do not hand-write multi-path SVG icons for "cloud" / "server" / "database" / "laptop" inside diagrams. They will be inconsistent in stroke width, viewbox, and visual weight with each other and with the rest of the app.

## Sequence and state diagrams: use Mermaid, not custom SVG

For **sequence diagrams** (time-axis traces — auth flows, request lifecycles) and **state machine diagrams** (states + labeled transitions — workflows, order lifecycles), use Mermaid. It's the industry standard, renders in GitHub/GitLab/Notion natively, and ships a tight syntax LLMs handle well.

```html
<!-- Mermaid via CDN, then a <pre class="mermaid"> block -->
<script type="module">
  import mermaid from "https://cdn.jsdelivr.net/npm/mermaid@10/dist/mermaid.esm.min.mjs";
  mermaid.initialize({ startOnLoad: true });
</script>
<pre class="mermaid">
sequenceDiagram
  Client->>Gateway: POST /auth/login
  Gateway->>AuthService: validate(creds)
  AuthService-->>Gateway: token
  Gateway-->>Client: 200 + token
</pre>
```

Mermaid is better than anything you'd hand-roll for these shapes, and forcing them into the SVG+DOM coordinate-space pattern would be a step backwards.

## Verify the diagram actually rendered correctly

Diagrams are exactly the case where the diff lies. After making one:

1. Load the page in a browser.
2. Screenshot it.
3. Look at the screenshot — do lines land on boxes, do labels fit, is anything overlapping?
4. For every connector path, list the (x,y) waypoints and check that no waypoint falls inside the rect of a card other than the source or target. Connector-through-unrelated-card is the second most common diagram bug after coordinate drift — a vertical line at x=1060 routed past a card at x=1020..1220 slices straight through it, and the screenshot will show the collision.
5. If you cannot load the app, say so explicitly. A diagram that "compiles" can still be visually broken.

The `frontend/design` skill covers the broader verify-visually discipline, including a copy-pasteable overflow probe — diagrams are exactly the case it's tuned for (connector lines landing outside their cards, node labels wrapping to a third line, the SVG container clipping a curve).

## Rules

- Search any available component library for diagram primitives before hand-rolling.
- When hand-rolling SVG+DOM, declare `W`, `H`, and named anchor points ONCE — use them for both the `viewBox` and the absolute-positioned nodes.
- Use `lucide-react` icons (or equivalent) inside diagram nodes — do not hand-write multi-path icon SVGs.
- For 1-to-many / fan-out / "anywhere" topologies, produce 2-3 layout options side by side instead of defaulting to a chain.
- Screenshot the rendered diagram and look at it before declaring done.
