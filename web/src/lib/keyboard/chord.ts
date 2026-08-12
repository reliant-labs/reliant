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
    key: event.key,
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

/** The first chord of a canonical sequence — the prefix that opens it. */
export function sequencePrefix(binding: string): string {
  return binding.split(" ")[0];
}
