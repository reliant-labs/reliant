# Cursor and Claude Code give you one agent. We give you a process.

| | Cursor / Claude Code | Reliant |
|---|---|---|
| Agents on a task | One | Many, orchestrated |
| Unit of work | Chat turn | Defined workflow |
| Parallel tasks | ✗ | ✓ multi-workspace |
| Role handoffs | Manual copy-paste | Automatic |
| Repeatable runs | ✗ ad-hoc | ✓ same process |

---
**Speaker Notes:**
So here's the thing single-agent tools can't get around. Cursor and Claude Code are both one agent in a chat window — you ask, it answers, you're the one holding the thread between steps. Reliant runs a process: a planner hands to researchers, researchers hand to implementers, and that happens across separate workspaces at the same time. That's not a feature they can bolt on, it's a different architecture. You'd have to rebuild the orchestration layer underneath the chat box, and the chat box is the whole product.

---
**Layout Hint:**
comparison-table

---
**Sources:**
- Reliant column: product architecture (multi-workflow, multi-workspace orchestration) — founder brief / system context.
- Cursor / Claude Code column: describes their public single-agent chat model. NOT drawn from a research file — phase-2-research/competitive-landscape.md was never created. Any specific competitor metric [needs verification] before this slide ships.
