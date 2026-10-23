---
name: verify
description: Visual verification before declaring frontend work done — the screenshot/viewport checklist and the auditOverflow browser probe that finds escaped text, clipped cards, and horizontal scroll the diff never shows.
---

# Verify visually before declaring done

Code that type-checks and tests that pass do not prove a UI looks right. Before reporting visual work complete:

1. Launch the app and load the page you changed (use the `run` skill if available, or the explicit dev-server command).
2. Take a screenshot. Look at it. Compare to the brief.
3. Check at least one other viewport width (mobile if the design is desktop-first, or vice versa).
4. Run the overflow probe below against the changed surface and read the report.
5. Check for overflow / text wrap / alignment regressions in the surrounding area, not just the changed component.

If you cannot run the app, say so explicitly — do not claim visual success based on the diff alone.

## Overflow probe

Headlines that escape their card, captions that wrap into a third line, columns that scroll horizontally on tablet — the diff never shows these. This snippet finds them. Run it in the browser via Chrome DevTools MCP (`mcp__chrome-devtools__evaluate_script`) against the root selector of the surface you changed:

```js
function auditOverflow(rootSelector) {
  const root = document.querySelector(rootSelector);
  if (!root) return { error: `no element matches ${rootSelector}` };

  const cssPath = (el) => {
    const parts = [];
    while (el && el.nodeType === 1 && parts.length < 6) {
      let s = el.tagName.toLowerCase();
      if (el.id) { s += "#" + el.id; parts.unshift(s); break; }
      if (el.className && typeof el.className === "string") {
        const cls = el.className.trim().split(/\s+/).slice(0, 2).join(".");
        if (cls) s += "." + cls;
      }
      parts.unshift(s);
      el = el.parentElement;
    }
    return parts.join(" > ");
  };

  const issues = [];
  const all = [root, ...root.querySelectorAll("*")];

  // (a) element's own content overflows its box
  for (const el of all) {
    if (el.scrollWidth - el.clientWidth > 1) {
      issues.push({ type: "scroll-x", sel: cssPath(el), over: el.scrollWidth - el.clientWidth, text: (el.innerText || "").slice(0, 80) });
    }
    if (el.scrollHeight - el.clientHeight > 1) {
      issues.push({ type: "scroll-y", sel: cssPath(el), over: el.scrollHeight - el.clientHeight, text: (el.innerText || "").slice(0, 80) });
    }
  }

  // (b) descendant's rect escapes a non-`overflow:visible` ancestor's rect
  function walk(el, parent) {
    if (parent) {
      const cr = el.getBoundingClientRect();
      const pr = parent.getBoundingClientRect();
      const cs = getComputedStyle(parent);
      const clipped = cs.overflow !== "visible" || cs.overflowX !== "visible" || cs.overflowY !== "visible";
      const escapes =
        cr.right > pr.right + 0.5 || cr.left < pr.left - 0.5 ||
        cr.bottom > pr.bottom + 0.5 || cr.top < pr.top - 0.5;
      if (escapes && !clipped) {
        issues.push({ type: "escapes-parent", sel: cssPath(el), parent: cssPath(parent), text: (el.innerText || el.tagName).slice(0, 80) });
      }
    }
    for (const c of el.children) walk(c, el);
  }
  walk(root, null);

  return { rootSelector, count: issues.length, issues: issues.slice(0, 40) };
}

auditOverflow("main");  // or a tighter selector for the surface you changed
```

Two classes of finding:

- `scroll-x` / `scroll-y` — an element's own content exceeds its box. Common cause: a long word/URL, a fixed-width child, or a table that doesn't shrink.
- `escapes-parent` — a child's bounding rect extends past an ancestor that isn't clipping. Common cause: absolute positioning gone wrong, negative margins, or a heading that overflows because its container shrank.

Run it at multiple viewports (resize via `mcp__chrome-devtools__resize_page` first) — a layout that's clean at 1440px often falls apart at 768px or 375px.

See also: `frontend/design` for the design rules this verifies against.
