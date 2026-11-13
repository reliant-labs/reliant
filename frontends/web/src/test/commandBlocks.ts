/**
 * The setup-surface rule: every shell command we show someone installing or
 * connecting Reliant (a machine, the CLI, an AI provider, a local model
 * server) sits in a CommandBlock with an always-visible Copy button.
 *
 * `expectEveryCommandCopyable` checks a rendered surface two ways:
 *
 *  - every `[data-command-block]` has a labelled "Copy …" button, never
 *    hidden behind hover;
 *  - no command-shaped `<code>`/`<pre>` sits OUTSIDE a CommandBlock, which is
 *    how a surface regressed before (a bare `<pre>` of `brew install ollama`,
 *    a `<p className="font-mono">` token command).
 *
 * "Command-shaped" means it starts with a program and a subcommand
 * (`reliant daemon start`, `brew install …`, `echo … | reliant …`). A bare
 * mention inside a sentence ("the `reliant` CLI") is prose, not a command.
 */
import { expect } from "vitest";

const COMMAND = /^(?:[A-Z_]+=\S+\s+)*(?:echo\s.+\|\s*)?(?:reliant|brew|ollama|curl|npm|npx|pnpm)\s+\S+/;

export function commandBlocks(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>("[data-command-block]")];
}

export function expectEveryCommandCopyable(container: HTMLElement, { atLeast = 1 } = {}) {
  const blocks = commandBlocks(container);
  expect(blocks.length, "command blocks rendered").toBeGreaterThanOrEqual(atLeast);
  for (const block of blocks) {
    const copy = [...block.querySelectorAll("button")].find((b) =>
      /^Copy\b/.test(b.getAttribute("aria-label") ?? ""),
    );
    const command = block.querySelector("code")?.textContent ?? "";
    expect(copy, `copy button for "${command}"`).toBeDefined();
    expect(copy!.className, `copy button for "${command}" is always visible`).not.toMatch(
      /(?:^|\s)(?:opacity-0|invisible|hidden)(?:\s|$)|group-hover/,
    );
  }
  const bare = [...container.querySelectorAll<HTMLElement>("code, pre, .font-mono")].filter(
    (el) => !el.closest("[data-command-block]") && COMMAND.test((el.textContent ?? "").trim()),
  );
  expect(
    bare.map((el) => el.textContent),
    "commands shown without a CommandBlock",
  ).toEqual([]);
}
