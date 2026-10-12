/**
 * Chord parsing and normalization.
 *
 * A "chord" is a single key press with modifiers (Cmd+Shift+P). A "sequence" is
 * an ordered list of chords typed in succession (Cmd+K then G). Both are
 * represented as a canonical string so they can be used as Map keys and
 * compared cheaply at dispatch time.
 *
 * Canonical form is `ctrl+meta+shift+alt+KEY` with modifiers always in that
 * order and omitted when false, e.g. `meta+shift+P`, `Escape`, `meta+K G`.
 * Sequences join chords with a single space.
 */

export interface Chord {
  key: string;
  ctrl?: boolean;
  meta?: boolean;
  shift?: boolean;
  alt?: boolean;
}

/** Keys whose event.key differs from how we want to write them in config. */
const EVENT_KEY_ALIASES: Record<string, string> = {
  ArrowDown: "Down",
  ArrowUp: "Up",
  ArrowLeft: "Left",
  ArrowRight: "Right",
  Esc: "Escape",
  " ": "Space",
};

/**
 * Shifted punctuation folded back to its unshifted key.
 *
 * Shift changes event.key for punctuation ("[" becomes "{"), so a binding
 * written as Cmd+Shift+[ would never match unless we fold it back. Letters are
 * handled separately by upper-casing.
 */
const SHIFTED_PUNCTUATION: Record<string, string> = {
  "{": "[",
  "}": "]",
  ":": ";",
  '"': "'",
  "<": ",",
  ">": ".",
  "?": "/",
  "~": "`",
  "|": "\\",
  _: "-",
  "+": "=",
};

/** Normalize a raw key name into the canonical spelling used by the registry. */
export function normalizeKey(rawKey: string, shiftKey = false): string {
  let key = EVENT_KEY_ALIASES[rawKey] ?? rawKey;

  if (shiftKey && SHIFTED_PUNCTUATION[key]) {
    key = SHIFTED_PUNCTUATION[key];
  }

  // Letters are case-insensitive: Shift+a and Shift+A must resolve alike.
  if (key.length === 1 && /[a-zA-Z]/.test(key)) {
    key = key.toUpperCase();
  }

  return key;
}

/** Serialize a chord to its canonical string form. */
export function chordToString(chord: Chord): string {
  const parts: string[] = [];
  if (chord.ctrl) parts.push("ctrl");
  if (chord.meta) parts.push("meta");
  if (chord.shift) parts.push("shift");
  if (chord.alt) parts.push("alt");
  parts.push(normalizeKey(chord.key, chord.shift));
  return parts.join("+");
}

/** Serialize an ordered chord list to its canonical sequence string. */
export function sequenceToString(chords: Chord[]): string {
  return chords.map(chordToString).join(" ");
}

/** Punctuation keys by physical position, for the `event.code` fallback. */
const CODE_PUNCTUATION: Record<string, string> = {
  BracketLeft: "[",
  BracketRight: "]",
  Semicolon: ";",
  Quote: "'",
  Comma: ",",
  Period: ".",
  Slash: "/",
  Backquote: "`",
  Backslash: "\\",
  Minus: "-",
  Equal: "=",
};

/** The key a physical position names on a US layout ("KeyB" -> "B"). */
function keyFromCode(code: string): string | undefined {
  if (/^Key[A-Z]$/.test(code)) return code.slice(3);
  if (/^Digit[0-9]$/.test(code)) return code.slice(5);
  return CODE_PUNCTUATION[code];
}

/**
 * True when `event.key` names something a binding can be written as: a single
 * printable ASCII character, or a named key ("Enter", "ArrowUp", "F5").
 *
 * What it rejects is what the layout or a modifier substituted for the key the
 * user pressed: the glyph macOS Option composes ("∫" for Option+B), a dead key
 * ("Dead" for Option+E), or a non-Latin letter ("л" for K on a Russian layout).
 */
function isBindableKey(key: string): boolean {
  if (key === "Dead" || key === "Unidentified" || key === "Process") {
    return false;
  }
  if (key.length === 1) return key >= " " && key <= "~";
  return true;
}

/**
 * The key a live event should match bindings by.
 *
 * Bindings are written with Latin letters, but `event.key` reports whatever the
 * keyboard produced: on macOS, Option turns Cmd+Option+B into "∫", and on a
 * Cyrillic, Greek or Hebrew layout every Cmd/Ctrl+letter arrives as a non-Latin
 * letter. Matching on `event.key` alone left those users with almost no working
 * shortcuts in a browser — while the desktop app's menu accelerators, which the
 * OS matches by physical key, kept the common ones working there.
 *
 * So when a command modifier is held and the reported key is not bindable, the
 * physical key (`event.code`) decides. A bindable `event.key` always wins, which
 * keeps layouts like AZERTY and Dvorak matching by the letter printed on the
 * key. Ctrl+Alt without Meta is left alone: on Windows that is AltGr, which
 * TYPES a character (AltGr+N is "ñ"), and treating it as a shortcut would eat
 * the user's text.
 */
export function eventKey(event: KeyboardEvent): string {
  if (isBindableKey(event.key)) return event.key;

  const commandHeld = event.metaKey || (event.ctrlKey && !event.altKey);
  if (!commandHeld) return event.key;

  return keyFromCode(event.code ?? "") ?? event.key;
}

/**
 * Build the canonical chord string for a live KeyboardEvent.
 *
 * Returns null for modifier-only presses (holding Shift on its own is not a
 * chord and must not be treated as one, or it would reset sequence state).
 */
export function eventToChordString(event: KeyboardEvent): string | null {
  if (
    event.key === "Control" ||
    event.key === "Meta" ||
    event.key === "Shift" ||
    event.key === "Alt"
  ) {
    return null;
  }

  return chordToString({
    key: eventKey(event),
    ctrl: event.ctrlKey,
    meta: event.metaKey,
    shift: event.shiftKey,
    alt: event.altKey,
  });
}

/**
 * Parse a human-authored binding string ("Cmd+Shift+P", "Cmd+K G") into
 * canonical form for the current platform.
 *
 * `Cmd` is the platform-primary modifier: Meta on macOS, Control elsewhere.
 * `Ctrl` is always literal Control. A binding using both (`Cmd+Ctrl+Up`)
 * therefore means Meta+Control on macOS; on Windows/Linux, where Cmd folds to
 * Control, we substitute Alt for the second modifier so the chord stays
 * physically typeable.
 */
export function parseBinding(binding: string, isMac: boolean): string {
  const chords = binding
    .trim()
    .split(/\s+/)
    .filter(Boolean)
    .map((token) => parseChordToken(token, isMac));

  return chords.join(" ");
}

function parseChordToken(token: string, isMac: boolean): string {
  // Split on "+" but keep a literal trailing "+" as the key (e.g. "Cmd++").
  const parts = token.split("+");
  const rawKey = parts.pop() || "+";

  let cmd = false;
  let ctrl = false;
  let shift = false;
  let alt = false;

  for (const part of parts) {
    switch (part.trim().toLowerCase()) {
      case "cmd":
      case "mod":
        cmd = true;
        break;
      case "ctrl":
      case "control":
        ctrl = true;
        break;
      case "shift":
        shift = true;
        break;
      case "alt":
      case "option":
        alt = true;
        break;
    }
  }

  const chord: Chord = { key: rawKey, shift };

  if (cmd && ctrl) {
    // Double-modifier binding: Meta+Ctrl on macOS, Ctrl+Alt elsewhere.
    if (isMac) {
      chord.meta = true;
      chord.ctrl = true;
    } else {
      chord.ctrl = true;
      chord.alt = true;
    }
  } else if (cmd) {
    if (isMac) chord.meta = true;
    else chord.ctrl = true;
  } else if (ctrl) {
    chord.ctrl = true;
  }

  if (alt) chord.alt = true;

  return chordToString(chord);
}

/** True when the canonical binding string contains more than one chord. */
export function isSequence(binding: string): boolean {
  return binding.includes(" ");
}

/** Modifier tokens in the canonical order they are serialized in. */
const MODIFIER_TOKENS = ["ctrl", "meta", "shift", "alt"] as const;

/**
 * Split a canonical chord into its modifiers and its key.
 *
 * Scans in canonical order rather than splitting on "+", so a chord whose key
 * IS "+" ("meta++") survives the round trip.
 */
function splitChord(chord: string): { mods: string[]; key: string } {
  const mods: string[] = [];
  let rest = chord;

  for (const mod of MODIFIER_TOKENS) {
    if (rest.startsWith(`${mod}+`)) {
      mods.push(mod);
      rest = rest.slice(mod.length + 1);
    }
  }

  return { mods, key: rest };
}

/**
 * Drop modifiers the user is still holding down from the sequence prefix.
 *
 * `Cmd+K G` gets typed two ways: release Cmd before pressing G, or keep Cmd
 * held and press G (the muscle memory every VS Code user brings with them).
 * The held form arrives as `meta+G`, which matches no binding, so the sequence
 * died silently and the whole Cmd+K family looked broken.
 *
 * Once a prefix is armed the next keystroke belongs to us alone, so a modifier
 * carried over from the prefix is noise rather than intent. Only modifiers that
 * were part of the prefix are dropped — a Shift the user added themselves is
 * still theirs, so a future `Cmd+K Shift+X` stays distinct.
 */
export function stripHeldModifiers(chord: string, prefix: string): string {
  const held = new Set(splitChord(prefix).mods);
  if (held.size === 0) return chord;

  const { mods, key } = splitChord(chord);
  const kept = mods.filter((mod) => !held.has(mod));
  if (kept.length === mods.length) return chord;

  return [...kept, key].join("+");
}

/**
 * A sequence's second chord as typed with the prefix's modifiers still held:
 * `meta+K W` becomes `meta+W`.
 *
 * stripHeldModifiers lets the dispatcher accept this form, but the dispatcher
 * only sees it if the browser lets it through — and this is the chord the
 * browser judges, not `W`. Returns undefined for a single-chord binding.
 */
export function heldSecondChord(binding: string): string | undefined {
  const chords = binding.split(" ");
  if (chords.length < 2) return undefined;

  const prefix = splitChord(chords[0]);
  const second = splitChord(chords[1]);
  const mods = MODIFIER_TOKENS.filter(
    (mod) => prefix.mods.includes(mod) || second.mods.includes(mod),
  );

  return [...mods, second.key].join("+");
}

/** The first chord of a canonical sequence — the prefix that opens it. */
export function sequencePrefix(binding: string): string {
  return binding.split(" ")[0];
}
