/**
 * Shortcut registry and resolution.
 *
 * The registry holds every known shortcut keyed by canonical binding string,
 * and answers one question at dispatch time: given this chord and this set of
 * active contexts, which shortcut should fire?
 *
 * Resolution is pure and synchronous so it can be unit-tested without a DOM.
 */

import { isSequence, sequencePrefix, stripHeldModifiers } from "./chord";
import { contextRank, isTextEntryContext } from "./contexts";

export interface ResolvedShortcut {
  id: string;
  handler: string;
  /** Canonical binding string, possibly multi-chord. */
  binding: string;
  context: string;
  /** Fire even when a text-entry surface has focus. */
  allowInInput?: boolean;
  /**
   * Let the event through to the browser/component after handling. Used by
   * bindings that augment rather than replace native behavior.
   */
  passthrough?: boolean;
}

export interface ResolveResult {
  kind: "match" | "sequence-prefix" | "none";
  shortcut?: ResolvedShortcut;
}

/**
 * Index of bindings for fast dispatch.
 *
 * `byBinding` maps a canonical binding to every shortcut claiming it across
 * contexts; the winner is chosen at resolve time by context precedence.
 * `sequencePrefixes` lets us recognize "Cmd+K" as the start of "Cmd+K G" and
 * swallow it rather than passing it through.
 */
export class ShortcutRegistry {
  private byBinding = new Map<string, ResolvedShortcut[]>();
  private sequencePrefixes = new Set<string>();

  constructor(shortcuts: ResolvedShortcut[] = []) {
    this.replaceAll(shortcuts);
  }

  replaceAll(shortcuts: ResolvedShortcut[]): void {
    this.byBinding.clear();
    this.sequencePrefixes.clear();

    for (const shortcut of shortcuts) {
      if (!shortcut.binding) continue;

      const existing = this.byBinding.get(shortcut.binding);
      if (existing) existing.push(shortcut);
      else this.byBinding.set(shortcut.binding, [shortcut]);

      if (isSequence(shortcut.binding)) {
        this.sequencePrefixes.add(sequencePrefix(shortcut.binding));
      }
    }

    // Most specific context first, so resolution can take the first match.
    for (const list of this.byBinding.values()) {
      list.sort((a, b) => contextRank(a.context) - contextRank(b.context));
    }
  }

  get size(): number {
    return this.byBinding.size;
  }

  /**
   * Resolve a chord (or accumulated sequence) against the active contexts.
   *
   * `pending` is the sequence typed so far; when non-empty the lookup is for
   * `pending + chord` rather than the bare chord.
   */
  resolve(
    chord: string,
    activeContexts: readonly string[],
    pending = "",
  ): ResolveResult {
    // A modifier still held from the prefix is not part of the second chord —
    // see stripHeldModifiers for why Cmd+K then Cmd+G must reach `meta+K G`.
    const completion = pending ? stripHeldModifiers(chord, pending) : chord;
    const lookup = pending ? `${pending} ${completion}` : chord;
    const candidates = this.byBinding.get(lookup);

    if (candidates) {
      const active = new Set(activeContexts);
      for (const candidate of candidates) {
        if (!active.has(candidate.context)) continue;

        // A bare key (no modifiers) must not steal ordinary typing. Sequences
        // are exempt: their prefix chord already established intent.
        if (
          !pending &&
          isBareKey(lookup) &&
          !candidate.allowInInput &&
          activeContexts.some(isTextEntryContext)
        ) {
          continue;
        }

        return { kind: "match", shortcut: candidate };
      }
    }

    // Not a completed binding — but it may open a sequence.
    if (!pending && this.sequencePrefixes.has(chord)) {
      return { kind: "sequence-prefix" };
    }

    return { kind: "none" };
  }

  /** All bindings claimed in a given context, for conflict reporting. */
  bindingsForContext(context: string): ResolvedShortcut[] {
    const out: ResolvedShortcut[] = [];
    for (const list of this.byBinding.values()) {
      for (const shortcut of list) {
        if (shortcut.context === context) out.push(shortcut);
      }
    }
    return out;
  }

  /**
   * Find shortcuts that shadow each other: same binding, same context.
   *
   * Same binding in *different* contexts is legal and intentional — that is the
   * whole point of the context model — so it is not reported here.
   */
  findConflicts(): Array<{ binding: string; shortcuts: ResolvedShortcut[] }> {
    const conflicts: Array<{ binding: string; shortcuts: ResolvedShortcut[] }> =
      [];

    for (const [binding, list] of this.byBinding) {
      const byContext = new Map<string, ResolvedShortcut[]>();
      for (const shortcut of list) {
        const group = byContext.get(shortcut.context);
        if (group) group.push(shortcut);
        else byContext.set(shortcut.context, [shortcut]);
      }
      for (const group of byContext.values()) {
        if (group.length > 1) conflicts.push({ binding, shortcuts: group });
      }
    }

    return conflicts;
  }
}

/**
 * True when a chord produces ordinary typed input rather than a command.
 *
 * Shift counts as bare: Shift+A is just "A" to a text field. Ctrl/Meta/Alt do
 * not, so those chords are safe to fire while typing.
 */
export function isBareKey(binding: string): boolean {
  return (
    !binding.startsWith("ctrl+") &&
    !binding.startsWith("meta+") &&
    !binding.includes("alt+") &&
    !binding.includes("ctrl+") &&
    !binding.includes("meta+")
  );
}
