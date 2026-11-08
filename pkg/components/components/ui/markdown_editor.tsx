import React from "react";

import { FormFieldContext } from "./form_field_context";

/**
 * MarkdownEditor — a plain <textarea> with a formatting toolbar, a Write /
 * Preview tab pair, a character and word count, and per-channel length
 * limits. No WYSIWYG engine and no dependency: the source the user types is
 * the value, which is what a post composer, a changelog or a description
 * field wants to store.
 *
 *   <MarkdownEditor
 *     label="Post"
 *     value={draft}
 *     onChange={setDraft}
 *     limits={[{ label: "Bluesky", max: 300 }, { label: "X", max: 280 }]}
 *   />
 *
 * Formatting goes through `document.execCommand("insertText")` where the
 * browser supports it, so Cmd/Ctrl+Z undoes a toolbar action like typing;
 * elsewhere it falls back to a plain value update.
 *
 * Shortcuts (Cmd on macOS, Ctrl elsewhere): B bold, I italic, K link,
 * E code, Shift+8 bulleted list, Shift+H heading. Enter on a list line
 * continues the list; Enter on an empty item ends it.
 *
 * The built-in preview renders a safe subset (headings, paragraphs, lists,
 * quotes, fenced code, bold, italic, inline code, http(s)/mailto links) as
 * React elements — never as HTML. Pass `renderPreview` to use your own
 * renderer (react-markdown, a channel-specific formatter).
 *
 * Limits count grapheme clusters (an emoji is one), the way Bluesky and X
 * count a post. Pass `measure` when a channel counts differently — X counts
 * every link as 23 characters, for instance.
 */
export interface MarkdownCharLimit {
  /** Who imposes the limit, shown beside the count: "Bluesky", "X", "LinkedIn hook". */
  label: string;
  /** The most characters the channel accepts. */
  max: number;
}

export type MarkdownEditorTab = "write" | "preview";

export type MarkdownFormat = "bold" | "italic" | "link" | "code" | "list" | "heading";

export interface MarkdownEditorProps {
  /** Controlled value. When set, the editor never updates itself; it calls `onChange`. */
  value?: string;
  /** Initial value when uncontrolled. Ignored if `value` is set. */
  defaultValue?: string;
  onChange?: (value: string) => void;
  /** Visible label above the editor; also names the textarea. */
  label?: React.ReactNode;
  /** Accessible name when there is no visible `label`. */
  ariaLabel?: string;
  placeholder?: string;
  /** Per-channel length limits. Each one shows its count and turns red once exceeded. */
  limits?: MarkdownCharLimit[];
  /** How the limits measure the text. Defaults to counting grapheme clusters. */
  measure?: (value: string) => number;
  /** Visible rows of the textarea. Defaults to 8. */
  rows?: number;
  disabled?: boolean;
  invalid?: boolean;
  /** The textarea's id. Inside a <FormField> it defaults to the field's id. */
  id?: string;
  /** Form field name, so the editor submits with a native <form>. */
  name?: string;
  /** Controlled tab. */
  tab?: MarkdownEditorTab;
  /** Initial tab when uncontrolled. Defaults to "write". */
  defaultTab?: MarkdownEditorTab;
  onTabChange?: (tab: MarkdownEditorTab) => void;
  /** Replace the built-in preview renderer. */
  renderPreview?: (markdown: string) => React.ReactNode;
  className?: string;
}

// ── Counting ───────────────────────────────────────────────────────────

const graphemes =
  typeof Intl !== "undefined" && "Segmenter" in Intl
    ? new Intl.Segmenter(undefined, { granularity: "grapheme" })
    : null;

function countGraphemes(text: string): number {
  if (!graphemes) return Array.from(text).length;
  return Array.from(graphemes.segment(text)).length;
}

function countWords(text: string): number {
  const trimmed = text.trim();
  return trimmed ? trimmed.split(/\s+/).length : 0;
}

// ── Formatting ─────────────────────────────────────────────────────────

/** A replacement of value[from, to) by text, then a selection to restore. */
interface Edit {
  from: number;
  to: number;
  text: string;
  selStart: number;
  selEnd: number;
}

const wrapMarkers: Partial<Record<MarkdownFormat, [string, string]>> = {
  bold: ["**", "bold text"],
  italic: ["_", "italic text"],
  code: ["`", "code"],
};

function lineStart(value: string, at: number): number {
  return value.lastIndexOf("\n", at - 1) + 1;
}

function lineEnd(value: string, at: number): number {
  const i = value.indexOf("\n", at);
  return i === -1 ? value.length : i;
}

function formatEdit(value: string, start: number, end: number, format: MarkdownFormat): Edit {
  const selected = value.slice(start, end);

  if (format === "link") {
    const text = selected || "link text";
    const insert = `[${text}](url)`;
    const urlAt = start + text.length + 3;
    return { from: start, to: end, text: insert, selStart: urlAt, selEnd: urlAt + 3 };
  }

  if (format === "code" && selected.includes("\n")) {
    const insert = "```\n" + selected + "\n```";
    return {
      from: start,
      to: end,
      text: insert,
      selStart: start + 4,
      selEnd: start + 4 + selected.length,
    };
  }

  const wrap = wrapMarkers[format];
  if (wrap) {
    const [marker, placeholder] = wrap;
    const m = marker.length;
    // Already wrapped on the outside: unwrap, keeping the selection.
    if (
      start >= m &&
      value.slice(start - m, start) === marker &&
      value.slice(end, end + m) === marker
    ) {
      return { from: start - m, to: end + m, text: selected, selStart: start - m, selEnd: end - m };
    }
    const inner = selected || placeholder;
    return {
      from: start,
      to: end,
      text: marker + inner + marker,
      selStart: start + m,
      selEnd: start + m + inner.length,
    };
  }

  // Line formats act on every line the selection touches.
  const from = lineStart(value, start);
  const to = lineEnd(value, end);
  const lines = value.slice(from, to).split("\n");
  let next: string[];
  if (format === "list") {
    const all = lines.every((l) => /^\s*[-*+]\s/.test(l));
    next = all ? lines.map((l) => l.replace(/^(\s*)[-*+]\s/, "$1")) : lines.map((l) => `- ${l}`);
  } else {
    const all = lines.every((l) => l.startsWith("## "));
    next = all
      ? lines.map((l) => l.slice(3))
      : lines.map((l) => `## ${l.replace(/^#{1,6}\s+/, "")}`);
  }
  const text = next.join("\n");
  return { from, to, text, selStart: from, selEnd: from + text.length };
}

const listItem = /^(\s*)([-*+]|(\d+)([.)]))(\s+)/;

/** Enter on a list line: continue the list, or end it on an empty item. */
function continueListEdit(value: string, caret: number): Edit | null {
  const from = lineStart(value, caret);
  const line = value.slice(from, lineEnd(value, caret));
  const m = listItem.exec(line);
  if (!m || caret < from + m[0].length) return null;
  if (line.slice(m[0].length).trim() === "") {
    // An empty item ends the list: clear its marker.
    return { from, to: from + line.length, text: "", selStart: from, selEnd: from };
  }
  const indent = m[1] ?? "";
  const marker = m[3] !== undefined ? `${Number(m[3]) + 1}${m[4] ?? "."}` : (m[2] ?? "-");
  const text = `\n${indent}${marker}${m[5] ?? " "}`;
  return {
    from: caret,
    to: caret,
    text,
    selStart: caret + text.length,
    selEnd: caret + text.length,
  };
}

// ── Preview ────────────────────────────────────────────────────────────

const safeHref = /^(https?:|mailto:|\/|#)/i;

const inlinePattern =
  /`([^`]+)`|\*\*([^*]+(?:\*(?!\*)[^*]*)*)\*\*|__([^_]+)__|\*([^*\s][^*]*)\*|(?<![\w])_([^_\s][^_]*)_(?![\w])|\[([^\]]+)\]\(([^)\s]+)\)/;

function renderInline(text: string, keyPrefix: string): React.ReactNode[] {
  const out: React.ReactNode[] = [];
  let rest = text;
  let k = 0;
  while (rest) {
    const m = inlinePattern.exec(rest);
    if (!m) {
      out.push(rest);
      break;
    }
    if (m.index > 0) out.push(rest.slice(0, m.index));
    const key = `${keyPrefix}-${k++}`;
    if (m[1] !== undefined) {
      out.push(
        <code key={key} className="rounded bg-surface-sunken px-1 py-0.5 font-mono text-[0.9em]">
          {m[1]}
        </code>,
      );
    } else if (m[2] !== undefined || m[3] !== undefined) {
      out.push(
        <strong key={key} className="font-semibold">
          {renderInline(m[2] ?? m[3] ?? "", key)}
        </strong>,
      );
    } else if (m[4] !== undefined || m[5] !== undefined) {
      out.push(<em key={key}>{renderInline(m[4] ?? m[5] ?? "", key)}</em>);
    } else {
      const label = m[6] ?? "";
      const href = m[7] ?? "";
      out.push(
        safeHref.test(href) ? (
          <a
            key={key}
            href={href}
            target="_blank"
            rel="noopener noreferrer"
            className="text-accent underline underline-offset-2"
          >
            {renderInline(label, key)}
          </a>
        ) : (
          <span key={key}>{renderInline(label, key)}</span>
        ),
      );
    }
    rest = rest.slice(m.index + m[0].length);
  }
  return out;
}

/** Text with its line breaks kept: a post's line breaks are part of the post. */
function renderLines(lines: string[], keyPrefix: string): React.ReactNode[] {
  return lines.flatMap((line, i) => {
    const parts = renderInline(line, `${keyPrefix}-${i}`);
    return i === 0 ? parts : [<br key={`${keyPrefix}-br-${i}`} />, ...parts];
  });
}

const headingStyles = [
  "text-xl font-semibold",
  "text-lg font-semibold",
  "text-base font-semibold",
  "text-sm font-semibold",
  "text-sm font-medium",
  "text-sm font-medium text-ink-muted",
];

const blockStart = /^(```|#{1,6}\s|\s*[-*+]\s|\s*\d+[.)]\s|>)/;

function renderMarkdown(source: string): React.ReactNode[] {
  const lines = source.replace(/\r\n?/g, "\n").split("\n");
  const blocks: React.ReactNode[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i] ?? "";
    const key = `b${i}`;
    if (line.trim() === "") {
      i++;
      continue;
    }
    if (line.startsWith("```")) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !(lines[i] ?? "").startsWith("```")) body.push(lines[i++] ?? "");
      i++; // the closing fence
      blocks.push(
        <pre
          key={key}
          className="overflow-x-auto rounded-md border border-border bg-surface-sunken p-3 font-mono text-xs"
        >
          <code>{body.join("\n")}</code>
        </pre>,
      );
      continue;
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      const level = (heading[1] ?? "#").length;
      const Tag = `h${level}` as "h1" | "h2" | "h3" | "h4" | "h5" | "h6";
      blocks.push(
        <Tag key={key} className={`text-ink ${headingStyles[level - 1] ?? ""}`}>
          {renderInline(heading[2] ?? "", key)}
        </Tag>,
      );
      i++;
      continue;
    }
    const ordered = /^\s*\d+[.)]\s/.test(line);
    if (ordered || /^\s*[-*+]\s/.test(line)) {
      const pattern = ordered ? /^\s*\d+[.)]\s+/ : /^\s*[-*+]\s+/;
      const items: string[] = [];
      while (i < lines.length && pattern.test(lines[i] ?? "")) {
        items.push((lines[i++] ?? "").replace(pattern, ""));
      }
      const List = ordered ? "ol" : "ul";
      blocks.push(
        <List key={key} className={`space-y-1 pl-5 ${ordered ? "list-decimal" : "list-disc"}`}>
          {items.map((item, j) => (
            <li key={j}>{renderInline(item, `${key}-${j}`)}</li>
          ))}
        </List>,
      );
      continue;
    }
    if (line.startsWith(">")) {
      const quoted: string[] = [];
      while (i < lines.length && (lines[i] ?? "").startsWith(">")) {
        quoted.push((lines[i++] ?? "").replace(/^>\s?/, ""));
      }
      blocks.push(
        <blockquote key={key} className="border-l-2 border-border-strong pl-3 text-ink-muted">
          {renderLines(quoted, key)}
        </blockquote>,
      );
      continue;
    }
    const para: string[] = [];
    while (i < lines.length && (lines[i] ?? "").trim() !== "") {
      if (para.length > 0 && blockStart.test(lines[i] ?? "")) break;
      para.push(lines[i++] ?? "");
    }
    blocks.push(<p key={key}>{renderLines(para, key)}</p>);
  }
  return blocks;
}

// ── Toolbar ────────────────────────────────────────────────────────────

// The platform is read through useSyncExternalStore so the server render and
// hydration agree ("Ctrl+"), and macOS then re-renders with "⌘".
const subscribeNever = () => () => {};
const readIsMac = () => /Mac|iPhone|iPad/.test(navigator.platform);
const serverIsMac = () => false;

const tools: Array<{ format: MarkdownFormat; label: string; keys: string; icon: React.ReactNode }> =
  [
    {
      format: "heading",
      label: "Heading",
      keys: "Shift+H",
      icon: <path d="M6 4v16M18 4v16M6 12h12" />,
    },
    {
      format: "bold",
      label: "Bold",
      keys: "B",
      icon: <path d="M7 4h6a4 4 0 0 1 0 8H7zM7 12h7a4 4 0 0 1 0 8H7z" />,
    },
    {
      format: "italic",
      label: "Italic",
      keys: "I",
      icon: <path d="M14 4h-4M14 20h-4M15 4 9 20" />,
    },
    {
      format: "link",
      label: "Link",
      keys: "K",
      icon: (
        <path d="M10 14a4 4 0 0 0 5.66 0l3-3a4 4 0 0 0-5.66-5.66l-1 1M14 10a4 4 0 0 0-5.66 0l-3 3a4 4 0 0 0 5.66 5.66l1-1" />
      ),
    },
    {
      format: "code",
      label: "Code",
      keys: "E",
      icon: <path d="m8 7-5 5 5 5M16 7l5 5-5 5" />,
    },
    {
      format: "list",
      label: "Bulleted list",
      keys: "Shift+8",
      icon: <path d="M9 6h11M9 12h11M9 18h11M4.5 6h.01M4.5 12h.01M4.5 18h.01" />,
    },
  ];

/** The format a keydown asks for, or null. */
function shortcutFormat(e: React.KeyboardEvent): MarkdownFormat | null {
  if (!(e.metaKey || e.ctrlKey) || e.altKey) return null;
  if (e.shiftKey) {
    if (e.code === "Digit8") return "list";
    if (e.code === "KeyH") return "heading";
    return null;
  }
  switch (e.key.toLowerCase()) {
    case "b":
      return "bold";
    case "i":
      return "italic";
    case "k":
      return "link";
    case "e":
      return "code";
    default:
      return null;
  }
}

// ── Component ──────────────────────────────────────────────────────────

export default function MarkdownEditor({
  value,
  defaultValue = "",
  onChange,
  label,
  ariaLabel,
  placeholder,
  limits,
  measure = countGraphemes,
  rows = 8,
  disabled,
  invalid,
  id,
  name,
  tab,
  defaultTab = "write",
  onTabChange,
  renderPreview,
  className,
}: MarkdownEditorProps) {
  const ctx = React.useContext(FormFieldContext);
  const autoId = React.useId();
  const textareaId = id ?? ctx?.id ?? `${autoId}-input`;
  const ids = {
    label: `${autoId}-label`,
    counts: `${autoId}-counts`,
    writeTab: `${autoId}-write-tab`,
    previewTab: `${autoId}-preview-tab`,
    writePanel: `${autoId}-write`,
    previewPanel: `${autoId}-preview`,
  };

  const [uncontrolledValue, setUncontrolledValue] = React.useState(defaultValue);
  const controlled = value !== undefined;
  const text = value ?? uncontrolledValue;

  const [uncontrolledTab, setUncontrolledTab] = React.useState<MarkdownEditorTab>(defaultTab);
  const tabControlled = tab !== undefined;
  const activeTab = tab ?? uncontrolledTab;

  const textareaRef = React.useRef<HTMLTextAreaElement>(null);
  const tabRefs = React.useRef<Record<MarkdownEditorTab, HTMLButtonElement | null>>({
    write: null,
    preview: null,
  });
  // A selection to restore after the next render, for the fallback path that
  // writes the value instead of inserting through the browser.
  const pendingSelection = React.useRef<[number, number] | null>(null);
  const [toolIndex, setToolIndex] = React.useState(0);
  const toolRefs = React.useRef<Array<HTMLButtonElement | null>>([]);
  const mod = React.useSyncExternalStore(subscribeNever, readIsMac, serverIsMac) ? "⌘" : "Ctrl+";

  React.useLayoutEffect(() => {
    const sel = pendingSelection.current;
    const el = textareaRef.current;
    if (!sel || !el) return;
    pendingSelection.current = null;
    el.setSelectionRange(sel[0], sel[1]);
  }, [text]);

  function commit(next: string) {
    if (!controlled) setUncontrolledValue(next);
    onChange?.(next);
  }

  function selectTab(next: MarkdownEditorTab) {
    if (!tabControlled) setUncontrolledTab(next);
    onTabChange?.(next);
  }

  function applyEdit(edit: Edit) {
    const el = textareaRef.current;
    if (!el) return;
    el.focus();
    el.setSelectionRange(edit.from, edit.to);
    const expected = el.value.slice(0, edit.from) + edit.text + el.value.slice(edit.to);
    let inserted = false;
    try {
      // Through the browser's own editing pipeline, so undo/redo includes it.
      // An empty insert is a no-op there, so a delete takes the fallback.
      inserted =
        edit.text !== "" &&
        typeof document.execCommand === "function" &&
        document.execCommand("insertText", false, edit.text);
    } catch {
      inserted = false;
    }
    if (inserted && el.value === expected) {
      el.setSelectionRange(edit.selStart, edit.selEnd);
      return;
    }
    pendingSelection.current = [edit.selStart, edit.selEnd];
    commit(expected);
  }

  function format(kind: MarkdownFormat) {
    const el = textareaRef.current;
    if (!el || disabled) return;
    applyEdit(formatEdit(el.value, el.selectionStart, el.selectionEnd, kind));
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    const kind = shortcutFormat(e);
    if (kind) {
      e.preventDefault();
      format(kind);
      return;
    }
    if (
      e.key === "Enter" &&
      !e.shiftKey &&
      !e.metaKey &&
      !e.ctrlKey &&
      !e.altKey &&
      !e.nativeEvent.isComposing
    ) {
      const el = e.currentTarget;
      if (el.selectionStart !== el.selectionEnd) return;
      const edit = continueListEdit(el.value, el.selectionStart);
      if (edit) {
        e.preventDefault();
        applyEdit(edit);
      }
    }
  }

  function handleToolbarKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    const last = tools.length - 1;
    let next: number | null = null;
    if (e.key === "ArrowRight") next = toolIndex === last ? 0 : toolIndex + 1;
    else if (e.key === "ArrowLeft") next = toolIndex === 0 ? last : toolIndex - 1;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = last;
    if (next === null) return;
    e.preventDefault();
    setToolIndex(next);
    toolRefs.current[next]?.focus();
  }

  function handleTabKeyDown(e: React.KeyboardEvent<HTMLButtonElement>) {
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return;
    e.preventDefault();
    const next: MarkdownEditorTab =
      e.key === "Home"
        ? "write"
        : e.key === "End"
          ? "preview"
          : activeTab === "write"
            ? "preview"
            : "write";
    selectTab(next);
    tabRefs.current[next]?.focus();
  }

  const chars = countGraphemes(text);
  const words = countWords(text);
  const measured = limits && limits.length > 0 ? measure(text) : 0;
  const over = (limits ?? []).filter((l) => measured > l.max);

  const tabClass = (active: boolean) =>
    `relative -mb-px border-b-2 px-1 pb-2 pt-2 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent ${
      active
        ? "border-ink font-medium text-ink"
        : "border-transparent text-ink-muted hover:text-ink"
    }`;

  return (
    <div className={className}>
      {label && (
        <label
          id={ids.label}
          htmlFor={textareaId}
          className="mb-1.5 block text-sm font-medium text-ink"
        >
          {label}
        </label>
      )}
      <div
        className={`rounded-md border bg-surface focus-within:ring-1 ${
          invalid
            ? "border-danger focus-within:border-danger focus-within:ring-danger"
            : "border-border-strong focus-within:border-accent focus-within:ring-accent"
        } ${disabled ? "bg-surface-muted" : ""}`}
      >
        <div className="flex items-end justify-between gap-2 border-b border-border px-3">
          <div role="tablist" aria-label="Editor mode" className="flex gap-4">
            <button
              ref={(el) => {
                tabRefs.current.write = el;
              }}
              type="button"
              role="tab"
              id={ids.writeTab}
              aria-selected={activeTab === "write"}
              aria-controls={ids.writePanel}
              tabIndex={activeTab === "write" ? 0 : -1}
              onClick={() => selectTab("write")}
              onKeyDown={handleTabKeyDown}
              className={tabClass(activeTab === "write")}
            >
              Write
            </button>
            <button
              ref={(el) => {
                tabRefs.current.preview = el;
              }}
              type="button"
              role="tab"
              id={ids.previewTab}
              aria-selected={activeTab === "preview"}
              aria-controls={ids.previewPanel}
              tabIndex={activeTab === "preview" ? 0 : -1}
              onClick={() => selectTab("preview")}
              onKeyDown={handleTabKeyDown}
              className={tabClass(activeTab === "preview")}
            >
              Preview
            </button>
          </div>
          <div
            role="toolbar"
            aria-label="Formatting"
            aria-controls={textareaId}
            onKeyDown={handleToolbarKeyDown}
            className={`flex items-center gap-0.5 py-1 ${activeTab === "write" ? "" : "invisible"}`}
          >
            {tools.map((tool, i) => (
              <button
                key={tool.format}
                ref={(el) => {
                  toolRefs.current[i] = el;
                }}
                type="button"
                aria-label={`${tool.label} (${mod}${tool.keys})`}
                title={`${tool.label} (${mod}${tool.keys})`}
                tabIndex={i === toolIndex ? 0 : -1}
                disabled={disabled}
                // Keep the textarea's selection: a mousedown on the button
                // would otherwise move focus and collapse it.
                onMouseDown={(e) => e.preventDefault()}
                onFocus={() => setToolIndex(i)}
                onClick={() => format(tool.format)}
                className="inline-flex h-7 w-7 items-center justify-center rounded text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-50"
              >
                <svg
                  aria-hidden="true"
                  viewBox="0 0 24 24"
                  className="h-4 w-4"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={2}
                  strokeLinecap="round"
                  strokeLinejoin="round"
                >
                  {tool.icon}
                </svg>
              </button>
            ))}
          </div>
        </div>

        <div
          role="tabpanel"
          id={ids.writePanel}
          aria-labelledby={ids.writeTab}
          hidden={activeTab !== "write"}
        >
          <textarea
            ref={textareaRef}
            id={textareaId}
            name={name}
            rows={rows}
            value={text}
            placeholder={placeholder}
            disabled={disabled}
            aria-label={label ? undefined : (ariaLabel ?? "Markdown")}
            aria-invalid={invalid || over.length > 0 || undefined}
            aria-describedby={ids.counts}
            onChange={(e) => commit(e.target.value)}
            onKeyDown={handleKeyDown}
            className="block w-full resize-y bg-transparent px-3 py-2 font-mono text-sm text-ink placeholder:text-ink-subtle focus:outline-none disabled:cursor-not-allowed disabled:text-ink-muted"
          />
        </div>
        <div
          role="tabpanel"
          id={ids.previewPanel}
          aria-labelledby={ids.previewTab}
          hidden={activeTab !== "preview"}
          tabIndex={0}
          className="min-h-24 space-y-3 px-3 py-2 text-sm text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent"
        >
          {activeTab === "preview" &&
            (text.trim() === "" ? (
              <p className="text-ink-subtle">Nothing to preview</p>
            ) : renderPreview ? (
              renderPreview(text)
            ) : (
              renderMarkdown(text)
            ))}
        </div>

        <div
          id={ids.counts}
          className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 border-t border-border px-3 py-1.5 text-xs text-ink-muted tabular-nums"
        >
          <span>
            {chars.toLocaleString()} {chars === 1 ? "character" : "characters"} ·{" "}
            {words.toLocaleString()} {words === 1 ? "word" : "words"}
          </span>
          {limits && limits.length > 0 && (
            <span className="flex flex-wrap gap-x-3">
              {limits.map((l) => {
                const exceeded = measured > l.max;
                return (
                  <span key={l.label} className={exceeded ? "font-medium text-danger-ink" : ""}>
                    {l.label} {measured.toLocaleString()}/{l.max.toLocaleString()}
                    {exceeded && ` · ${(measured - l.max).toLocaleString()} over`}
                  </span>
                );
              })}
            </span>
          )}
        </div>
      </div>
      {/* Announced once when a limit is crossed, not on every keystroke. */}
      <span className="sr-only" aria-live="polite">
        {over.length > 0 ? `Over the ${over.map((l) => l.label).join(" and ")} limit` : ""}
      </span>
    </div>
  );
}
