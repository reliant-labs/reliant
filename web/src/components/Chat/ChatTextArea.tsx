import {
  useRef,
  useCallback,
  useEffect,
  useLayoutEffect,
  useState,
  forwardRef,
  useImperativeHandle,
} from "react";
import { useActiveChatId } from "../../store/chatStoreHooks";
import { useChatNavigationStore } from "../../store/chatNavigationStore";
import { getFileExtension } from "../../lib/fileUtils";
import { useFileOpener } from "../../lib/fileOpener";
import { parseFilePath } from "../../lib/filePath";
import "./chat-composer.css";

interface ChatTextAreaProps {
  value: string;
  onChange: (value: string) => void;
  onSend: () => void;
  onStop?: () => void;
  disabled?: boolean;
  isStreaming?: boolean;
  placeholder?: string;
  chatId?: string;
}

const MARKER_PATTERN = /\[\[([^\]]+):(\d+)-(\d+)\]\]/g;

type MarkerInfo = {
  marker: string;
  filePath: string;
  fileName: string;
  startLine: number;
  endLine: number;
  language?: string;
};

// Example prompts that rotate randomly
const EXAMPLE_PROMPTS = [
  "Add authentication to my API",
  "Refactor this component to use hooks",
  "Fix the bug in the checkout flow",
  "Write tests for the user service",
  "Optimize this database query",
  "Add dark mode to my app",
  "Create a REST API for users",
  "Set up CI/CD pipeline",
  "Improve the error handling",
  "Add input validation to this form",
  "Convert this to TypeScript",
  "Make this component responsive",
  "Add logging to track errors",
  "Implement caching for this endpoint",
  "Review and improve this code",
  "Build a user dashboard",
  "Add pagination to this list",
  "Create a file upload feature",
  "Implement real-time notifications",
  "Add search functionality",
  "Build a payment integration",
  "Create an admin panel",
  "Add email verification",
  "Implement password reset",
  "Build a comment system",
  "Add rate limiting",
  "Create a webhook handler",
  "Implement OAuth login",
  "Add database migrations",
  "Build a chat feature",
  "Create export to CSV",
  "Add image compression",
  "Implement lazy loading",
  "Build a filter system",
  "Add analytics tracking",
  "Create a scheduling system",
  "Implement drag and drop",
  "Add multi-language support",
  "Build a notification system",
  "Create API documentation",
  "Add error monitoring",
  "Implement data validation",
  "Build a settings page",
  "Add role-based permissions",
  "Create a backup system",
  "Implement infinite scroll",
  "Add keyboard shortcuts",
  "Build a reporting dashboard",
  "Create a mobile app version",
  "Add WebSocket support",
];

function getLanguageColor(lang?: string): string {
  const colors: Record<string, string> = {
    ts: "bg-blue-500",
    js: "bg-yellow-500",
    tsx: "bg-blue-600",
    jsx: "bg-yellow-600",
    py: "bg-green-500",
    go: "bg-cyan-500",
    rs: "bg-orange-500",
    java: "bg-red-500",
    cpp: "bg-purple-500",
    c: "bg-gray-500",
    html: "bg-orange-400",
    css: "bg-blue-400",
    json: "bg-green-400",
    yaml: "bg-purple-400",
    yml: "bg-purple-400",
    md: "bg-gray-400",
  };
  return colors[lang?.toLowerCase() || ""] || "bg-gray-500";
}

function getChipBgColor(lang?: string): string {
  const colors: Record<string, string> = {
    ts: "bg-blue-500/10 border-blue-500/30",
    js: "bg-yellow-500/10 border-yellow-500/30",
    tsx: "bg-blue-600/10 border-blue-600/30",
    jsx: "bg-yellow-600/10 border-yellow-600/30",
    py: "bg-green-500/10 border-green-500/30",
    go: "bg-cyan-500/10 border-cyan-500/30",
    rs: "bg-orange-500/10 border-orange-500/30",
    java: "bg-red-500/10 border-red-500/30",
    cpp: "bg-purple-500/10 border-purple-500/30",
    c: "bg-gray-500/10 border-gray-500/30",
    html: "bg-orange-400/10 border-orange-400/30",
    css: "bg-blue-400/10 border-blue-400/30",
    json: "bg-green-400/10 border-green-400/30",
    yaml: "bg-purple-400/10 border-purple-400/30",
    yml: "bg-purple-400/10 border-purple-400/30",
    md: "bg-gray-400/10 border-gray-400/30",
  };
  return colors[lang?.toLowerCase() || ""] || "bg-gray-500/10 border-gray-500/30";
}

function parseMarkers(value: string): Array<{ type: "text"; text: string } | { type: "marker"; info: MarkerInfo }> {
  const parts: Array<{ type: "text"; text: string } | { type: "marker"; info: MarkerInfo }> = [];
  let lastIndex = 0;
  MARKER_PATTERN.lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = MARKER_PATTERN.exec(value)) !== null) {
    const [marker, filePath, startLineStr, endLineStr] = match;
    const startLine = parseInt(startLineStr, 10);
    const endLine = parseInt(endLineStr, 10);

    if (match.index > lastIndex) {
      parts.push({ type: "text", text: value.slice(lastIndex, match.index) });
    }

    const fileName = filePath.split("/").pop() || filePath;
    const language = getFileExtension(fileName);
    parts.push({
      type: "marker",
      info: { marker, filePath, fileName, startLine, endLine, language },
    });
    lastIndex = match.index + marker.length;
  }

  if (lastIndex < value.length) {
    parts.push({ type: "text", text: value.slice(lastIndex) });
  }

  return parts;
}

function createMarkerElement(info: MarkerInfo): HTMLElement {
  const el = document.createElement("span");
  el.setAttribute("contenteditable", "false");
  el.dataset.marker = info.marker;
  el.dataset.filePath = info.filePath;
  el.dataset.startLine = String(info.startLine);
  el.dataset.endLine = String(info.endLine);

  const language = info.language || getFileExtension(info.fileName);
  const languageLabel = (language || "??").toUpperCase();
  const badgeText = languageLabel.length <= 3 ? languageLabel : languageLabel.slice(0, 2);
  const lineRange = info.startLine === info.endLine ? `${info.startLine}` : `${info.startLine}-${info.endLine}`;

  el.className = [
    // Match the caret/line-height (text-sm => 20px). This prevents the chip
    // from bumping the composer height and keeps it aligned with the cursor.
    // IMPORTANT: box-border ensures the 1px border is included in h-5.
    "inline-flex items-center gap-1.5 h-5 px-2 py-0 rounded-md border box-border",
    getChipBgColor(language),
    "text-sm font-medium cursor-pointer select-none align-middle",
    "hover:opacity-80 transition-opacity",
    "group/token",
  ].join(" ");

  const badge = document.createElement("span");
  badge.className = [
    "h-4 px-1.5 rounded flex items-center justify-center",
    "text-white text-[9px] font-bold leading-none",
    getLanguageColor(language),
  ].join(" ");
  badge.textContent = badgeText;

  const label = document.createElement("span");
  label.className = "text-foreground";
  label.textContent = `${info.fileName} (${lineRange})`;

  const remove = document.createElement("button");
  remove.type = "button";
  remove.dataset.removeMarker = "true";
  remove.className = [
    "ml-1 text-muted-foreground/60 hover:text-foreground",
    "opacity-0 group-hover/token:opacity-100 transition-opacity",
    "text-xs leading-none",
  ].join(" ");
  remove.textContent = "×";
  remove.title = "Remove";

  el.appendChild(badge);
  el.appendChild(label);
  el.appendChild(remove);

  return el;
}

function serializeFromDom(root: HTMLElement): string {
  const walk = (node: Node): string => {
    if (node.nodeType === Node.TEXT_NODE) {
      return node.textContent || "";
    }
    if (node.nodeType !== Node.ELEMENT_NODE) return "";

    const el = node as HTMLElement;
    if (el.dataset?.marker) return el.dataset.marker;
    if (el.tagName === "BR") return "\n";

    // Treat block-ish nodes as newline separators
    const isBlock = el.tagName === "DIV" || el.tagName === "P";
    const text = Array.from(el.childNodes).map(walk).join("");
    return isBlock ? text + "\n" : text;
  };

  let out = Array.from(root.childNodes).map(walk).join("");
  // Normalize trailing newline added by block wrappers
  out = out.replace(/\n+$/g, "");
  return out;
}

function isEmptyOrBrOnly(el: HTMLElement): boolean {
  // Treat elements with only whitespace text and/or <br> as empty.
  for (const child of Array.from(el.childNodes)) {
    if (child.nodeType === Node.TEXT_NODE) {
      if ((child.textContent || "").trim() !== "") return false;
      continue;
    }
    if (child.nodeType === Node.ELEMENT_NODE) {
      const c = child as HTMLElement;
      if (c.tagName === "BR") continue;
      // Marker tokens count as content
      if (c.dataset?.marker) return false;
      if (!isEmptyOrBrOnly(c)) return false;
      continue;
    }
    return false;
  }
  return true;
}

function normalizeComposerDom(root: HTMLElement) {
  // 1) Unwrap stray <div>/<p> wrappers that browsers create in contenteditable,
  // keeping explicit line breaks via <br>.
  const unwrapOneLevel = () => {
    const hasMeaningfulContent = (node: Node | null): boolean => {
      if (!node) return false;

      if (node.nodeType === Node.TEXT_NODE) {
        const text = node.textContent ?? "";
        // Preserve normal spaces as meaningful user input; only ignore empty/newline artifacts.
        return text !== "" && !/^[\n\r\t]+$/.test(text);
      }

      if (node.nodeType === Node.ELEMENT_NODE) {
        const el = node as HTMLElement;
        if (el.tagName === "BR") return false;
        return !isEmptyOrBrOnly(el);
      }

      return false;
    };

    const children = Array.from(root.childNodes);
    for (const node of children) {
      if (node.nodeType !== Node.ELEMENT_NODE) continue;
      const el = node as HTMLElement;
      if (el.tagName !== "DIV" && el.tagName !== "P") continue;

      // Insert a <br> before unwrapped block content whenever there is meaningful
      // content immediately before this block and that content isn't already a line break.
      const prev = el.previousSibling;
      const needsBreak = hasMeaningfulContent(prev);

      if (needsBreak) {
        root.insertBefore(document.createElement("br"), el);
      }

      // Move children out of the block
      while (el.firstChild) {
        root.insertBefore(el.firstChild, el);
      }
      el.remove();
    }
  };

  unwrapOneLevel();

  // 2) Remove leading/trailing empty blocks/BRs that cause "stuck on next line" artifacts.
  // This is what happens when you delete all text before a token and the browser
  // leaves an empty line wrapper behind.
  const trimEdgeEmpties = () => {
    const removeLeading = () => {
      while (root.firstChild) {
        const n = root.firstChild;
        if (n.nodeType === Node.TEXT_NODE) {
          const text = n.textContent ?? "";
          // IMPORTANT: Do not strip normal spaces here. If we remove whitespace-only
          // text nodes, the user can't type a space at the start/end of the composer
          // (it gets deleted immediately on input), which feels like the Space key
          // is broken after inserting a context chip.
          //
          // We *do* strip newline/tab-only nodes which are typically browser artifacts.
          if (text === "" || /^[\n\r\t]+$/.test(text)) {
            n.remove();
            continue;
          }
          break;
        }
        if (n.nodeType === Node.ELEMENT_NODE) {
          const el = n as HTMLElement;
          // Don't remove leading <br> - user may have pressed Shift+Enter at the start
          if ((el.tagName === "DIV" || el.tagName === "P") && isEmptyOrBrOnly(el)) {
            el.remove();
            continue;
          }
        }
        break;
      }
    };

    const removeTrailing = () => {
      while (root.lastChild) {
        const n = root.lastChild;
        if (n.nodeType === Node.TEXT_NODE) {
          const text = n.textContent ?? "";
          // Keep space-only nodes so trailing spaces can be typed normally.
          // Strip newline/tab-only nodes which are typically browser artifacts.
          if (text === "" || /^[\n\r\t]+$/.test(text)) {
            n.remove();
            continue;
          }
          break;
        }
        if (n.nodeType === Node.ELEMENT_NODE) {
          const el = n as HTMLElement;
          // Don't remove trailing <br> - user may have pressed Shift+Enter at the end
          if ((el.tagName === "DIV" || el.tagName === "P") && isEmptyOrBrOnly(el)) {
            el.remove();
            continue;
          }
        }
        break;
      }
    };

    removeLeading();
    removeTrailing();
  };

  trimEdgeEmpties();
}

function setCaretToEnd(el: HTMLElement) {
  el.focus();
  const range = document.createRange();
  range.selectNodeContents(el);
  range.collapse(false);
  const sel = window.getSelection();
  sel?.removeAllRanges();
  sel?.addRange(range);
}

function setCaretToStart(el: HTMLElement) {
  el.focus();
  const range = document.createRange();
  range.selectNodeContents(el);
  range.collapse(true);
  const sel = window.getSelection();
  sel?.removeAllRanges();
  sel?.addRange(range);
}

/**
 * Scroll the caret into view within a scrollable contentEditable element.
 * This ensures the cursor remains visible when typing at the bottom of an overflowing textarea.
 */
function scrollCaretIntoView(el: HTMLElement) {
  const sel = window.getSelection();
  if (!sel || sel.rangeCount === 0) return;

  const range = sel.getRangeAt(0);
  if (!el.contains(range.commonAncestorContainer)) return;

  // Get caret position relative to viewport
  const caretRect = range.getBoundingClientRect();
  const elRect = el.getBoundingClientRect();

  // Check if caret is below the visible area
  if (caretRect.bottom > elRect.bottom) {
    el.scrollTop += caretRect.bottom - elRect.bottom + 4; // +4 for padding
  }
  // Check if caret is above the visible area
  else if (caretRect.top < elRect.top) {
    el.scrollTop -= elRect.top - caretRect.top + 4;
  }
}

function insertNodeAtCursor(root: HTMLElement, node: Node) {
  const sel = window.getSelection();
  const range = sel?.rangeCount ? sel.getRangeAt(0) : null;

  if (!range || !root.contains(range.commonAncestorContainer)) {
    root.appendChild(node);
    return;
  }

  range.deleteContents();
  range.insertNode(node);

  // Move cursor after inserted node
  range.setStartAfter(node);
  range.collapse(true);
  sel?.removeAllRanges();
  sel?.addRange(range);
}

function insertPlainTextAtCursor(root: HTMLElement, text: string): boolean {
  if (!text) return false;

  const sel = window.getSelection();
  let range = sel?.rangeCount ? sel.getRangeAt(0) : null;

  // If selection isn't inside editor, append at end
  if (!range || !root.contains(range.commonAncestorContainer)) {
    setCaretToEnd(root);
    const nextSel = window.getSelection();
    range = nextSel?.rangeCount ? nextSel.getRangeAt(0) : null;
  }

  if (!range) return false;

  const lines = text.split(/\r\n|\n|\r/);
  const fragment = document.createDocumentFragment();
  const insertedNodes: Node[] = [];

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.length > 0) {
      const textNode = document.createTextNode(line);
      insertedNodes.push(textNode);
      fragment.appendChild(textNode);
    }

    if (i < lines.length - 1) {
      const br = document.createElement("br");
      insertedNodes.push(br);
      fragment.appendChild(br);
    }
  }

  if (insertedNodes.length === 0) {
    return false;
  }

  range.deleteContents();
  range.insertNode(fragment);

  // Move caret after the last inserted node.
  const lastInserted = insertedNodes[insertedNodes.length - 1];
  const nextRange = document.createRange();
  nextRange.setStartAfter(lastInserted);
  nextRange.collapse(true);
  sel?.removeAllRanges();
  sel?.addRange(nextRange);

  return true;
}

export const __chatTextAreaTestUtils = {
  normalizeComposerDom,
  serializeFromDom,
};

export const ChatTextArea = forwardRef<HTMLDivElement, ChatTextAreaProps>(function ChatTextArea({
  value,
  onChange,
  onSend,
  onStop,
  disabled = false,
  isStreaming = false,
  placeholder,
  chatId: _chatId,
}, ref) {
  const editorRef = useRef<HTMLDivElement>(null);
  const skipNextSyncRef = useRef(false);
  const skipNormalizeNextRef = useRef(false);
  const savedCursorPositionRef = useRef<{ container: Node; offset: number } | null>(null);
  const wasFocusedBeforeBlurRef = useRef(false);
  const openFile = useFileOpener();

  // Expose the internal ref to the parent
  useImperativeHandle(ref, () => editorRef.current!, []);
  const activeChatId = useActiveChatId();

  // Pick a random example prompt and update when chat changes
  const [examplePrompt, setExamplePrompt] = useState(() => {
    const randomIndex = Math.floor(Math.random() * EXAMPLE_PROMPTS.length);
    return EXAMPLE_PROMPTS[randomIndex];
  });

  // Re-randomize when switching chats (not chatId, because new chats have no chatId yet)
  useEffect(() => {
    const randomIndex = Math.floor(Math.random() * EXAMPLE_PROMPTS.length);
    setExamplePrompt(EXAMPLE_PROMPTS[randomIndex]);
  }, [activeChatId]);
  
  const defaultPlaceholder = `Ask Reliant, e.g. "${examplePrompt}"`;

  const adjustHeight = useCallback(() => {
    const el = editorRef.current;
    if (!el) return;

    const computedStyle = window.getComputedStyle(el);
    const lineHeight = parseFloat(computedStyle.lineHeight) || 20;
    const paddingTop = parseFloat(computedStyle.paddingTop) || 0;
    const paddingBottom = parseFloat(computedStyle.paddingBottom) || 0;
    const minHeight = lineHeight + paddingTop + paddingBottom;
    const maxHeight = 300;

    const currentHeight = el.offsetHeight;
    const hasContent = serializeFromDom(el).length > 0;

    // If already at max height and content still overflows, skip the height reset dance.
    // This prevents scroll position jumping when typing in a full textarea.
    // scrollHeight on a scrollable element gives us the full content height even with fixed height,
    // so we can check if content still overflows without changing the height.
    if (currentHeight >= maxHeight && hasContent && el.scrollHeight >= maxHeight) {
      return;
    }

    // Preserve scroll position to prevent jarring jumps
    const scrollTop = el.scrollTop;
    const scrollLeft = el.scrollLeft;

    el.style.height = "auto";
    const newHeight = hasContent
      ? Math.min(Math.max(el.scrollHeight, minHeight), maxHeight)
      : minHeight;
    el.style.height = `${newHeight}px`;

    // Restore scroll position after height adjustment
    el.scrollTop = scrollTop;
    el.scrollLeft = scrollLeft;
  }, []);

  const handleFocus = useCallback(() => {
    // When text input is focused, just ensure chat is active
    // Don't clear viewer - allow both to remain visible
    if (activeChatId) {
      useChatNavigationStore.getState().navigateToChat(activeChatId);
    }

    const el = editorRef.current;
    if (!el) return;

    // If we have a saved cursor position from window blur, restore it
    if (wasFocusedBeforeBlurRef.current && savedCursorPositionRef.current) {
      const hasContent = serializeFromDom(el).trim().length > 0;
      if (hasContent) {
        requestAnimationFrame(() => {
          // Double-check the input is still focused
          if (document.activeElement !== el) {
            savedCursorPositionRef.current = null;
            wasFocusedBeforeBlurRef.current = false;
            return;
          }
          
          try {
            const { container, offset } = savedCursorPositionRef.current!;
            // Check if the saved container is still in the DOM
            if (el.contains(container) || container === el) {
              const range = document.createRange();
              range.setStart(container, offset);
              range.collapse(true);
              const sel = window.getSelection();
              sel?.removeAllRanges();
              sel?.addRange(range);
            } else {
              // Container is gone (e.g., DOM was rebuilt), fall back to end
              setCaretToEnd(el);
            }
          } catch (e) {
            // If restoration fails, fall back to end
            setCaretToEnd(el);
          }
          // Clear saved position after attempting restore
          savedCursorPositionRef.current = null;
          wasFocusedBeforeBlurRef.current = false;
        });
        return;
      }
    }

    // When empty, force caret to the start (Cursor-like).
    if (value.trim().length === 0) {
      requestAnimationFrame(() => setCaretToStart(el));
    }
  }, [activeChatId, value]);

  // Keep DOM in sync with the external value *only when needed*
  useLayoutEffect(() => {
    const el = editorRef.current;
    if (!el) return;
    
    // Skip rebuild if we just inserted a line break (Shift+Enter) or pasted
    if (skipNextSyncRef.current) {
      skipNextSyncRef.current = false;
      const current = serializeFromDom(el);
      // If the serialized DOM matches the value, we're in sync - skip rebuild
      if (current === value) {
        return;
      }
      // DOM doesn't match value — fall through to rebuild so we don't get stuck
    }
    
    const current = serializeFromDom(el);
    if (current === value) return;

    // Preserve scroll position before rebuilding DOM
    const scrollTop = el.scrollTop;
    const scrollLeft = el.scrollLeft;

    // Rebuild DOM from value string (markers become inline tokens)
    el.innerHTML = "";
    const parts = parseMarkers(value);
    for (const part of parts) {
      if (part.type === "text") {
        // Split text by newlines and insert <br> tags
        // Preserve empty lines (consecutive newlines)
        const lines = part.text.split("\n");
        for (let i = 0; i < lines.length; i++) {
          // Always add text node, even if empty (to preserve line structure)
          el.appendChild(document.createTextNode(lines[i]));
          // Add <br> after each line except the last
          if (i < lines.length - 1) {
            el.appendChild(document.createElement("br"));
          }
        }
      } else {
        el.appendChild(createMarkerElement(part.info));
      }
    }
    el.dataset.empty = value.trim().length === 0 ? "true" : "false";
    adjustHeight();
    
    // Restore scroll position after rebuild
    el.scrollTop = scrollTop;
    el.scrollLeft = scrollLeft;
  }, [value, adjustHeight]);

  const emitChangeFromDom = useCallback(() => {
    const el = editorRef.current;
    if (!el) return;
    
    // Skip normalization if we just inserted a line break (Shift+Enter)
    // This prevents any interference with the <br> tag the browser just inserted
    if (!skipNormalizeNextRef.current) {
      normalizeComposerDom(el);
    } else {
      skipNormalizeNextRef.current = false;
    }
    
    const raw = serializeFromDom(el);
    const isEmpty = raw.trim().length === 0;

    // When the user deletes everything, browsers often leave behind <br> or
    // block wrappers inside contenteditable. That can push the caret to the
    // "end" of the placeholder visually. Normalize to a truly empty DOM.
    if (isEmpty) {
      el.innerHTML = "";
    }

    const next = isEmpty ? "" : raw;
    el.dataset.empty = isEmpty ? "true" : "false";
    adjustHeight();
    scrollCaretIntoView(el);
    
    onChange(next);

    if (isEmpty && document.activeElement === el) {
      requestAnimationFrame(() => setCaretToStart(el));
    }
  }, [onChange, adjustHeight]);

  const insertMarker = useCallback((marker: string) => {
    const el = editorRef.current;
    if (!el) return;

    // Parse marker
    MARKER_PATTERN.lastIndex = 0;
    const m = MARKER_PATTERN.exec(marker);
    if (!m) return;
    const [, filePath, startLineStr, endLineStr] = m;
    const startLine = parseInt(startLineStr, 10);
    const endLine = parseInt(endLineStr, 10);
    const fileName = filePath.split("/").pop() || filePath;
    const language = getFileExtension(fileName);

    // Ensure focus, then insert a token + trailing space
    el.focus();
    const token = createMarkerElement({
      marker,
      filePath,
      fileName,
      startLine,
      endLine,
      language,
    });

    insertNodeAtCursor(el, token);
    insertNodeAtCursor(el, document.createTextNode(" "));
    emitChangeFromDom();
  }, [emitChangeFromDom]);

  // Save and restore cursor position when window loses/regains focus
  useEffect(() => {
    const handleWindowBlur = () => {
      const el = editorRef.current;
      if (!el) return;
      
      // Save cursor position if the chat input was focused
      if (document.activeElement === el) {
        wasFocusedBeforeBlurRef.current = true;
        const sel = window.getSelection();
        const range = sel?.rangeCount ? sel.getRangeAt(0) : null;
        if (range && range.collapsed) {
          savedCursorPositionRef.current = {
            container: range.startContainer,
            offset: range.startOffset,
          };
        } else {
          savedCursorPositionRef.current = null;
        }
      } else {
        wasFocusedBeforeBlurRef.current = false;
        savedCursorPositionRef.current = null;
      }
    };

    const handleWindowFocus = () => {
      const el = editorRef.current;
      if (!el) return;
      
      // If input is already focused when window regains focus, restore immediately
      // Otherwise, let handleFocus callback restore when input gets focused
      if (wasFocusedBeforeBlurRef.current && savedCursorPositionRef.current && document.activeElement === el) {
        const hasContent = serializeFromDom(el).trim().length > 0;
        if (hasContent) {
          // Wait a bit for the DOM to be ready, then restore cursor position
          requestAnimationFrame(() => {
            // Double-check the input is still focused (user might have clicked away)
            if (document.activeElement !== el) {
              // Don't clear the flag - let handleFocus try to restore when input gets focused
              return;
            }
            
            try {
              const { container, offset } = savedCursorPositionRef.current!;
              // Check if the saved container is still in the DOM
              if (el.contains(container) || container === el) {
                const range = document.createRange();
                range.setStart(container, offset);
                range.collapse(true);
                const sel = window.getSelection();
                sel?.removeAllRanges();
                sel?.addRange(range);
              } else {
                // Container is gone (e.g., DOM was rebuilt), fall back to end
                setCaretToEnd(el);
              }
            } catch (e) {
              // If restoration fails, fall back to end
              setCaretToEnd(el);
            }
            // Clear saved position after attempting restore
            savedCursorPositionRef.current = null;
            wasFocusedBeforeBlurRef.current = false;
          });
        } else {
          // No content, clear saved position
          savedCursorPositionRef.current = null;
          wasFocusedBeforeBlurRef.current = false;
        }
      }
      // If input isn't focused yet, keep the flag set so handleFocus can restore it
    };

    window.addEventListener("blur", handleWindowBlur);
    window.addEventListener("focus", handleWindowFocus);
    return () => {
      window.removeEventListener("blur", handleWindowBlur);
      window.removeEventListener("focus", handleWindowFocus);
    };
  }, []);

  // Listen for context marker inserts (from file viewer)
  useEffect(() => {
    const handleAddMarker = (e: Event) => {
      const customEvent = e as CustomEvent<{ marker: string }>;
      const marker = customEvent.detail?.marker;
      if (!marker) return;

      const el = editorRef.current;
      if (!el) return;
      // Insert at current caret position; if not focused, append to end.
      if (document.activeElement !== el) {
        setCaretToEnd(el);
      }
      insertMarker(marker);
    };

    const handleFocusEvent = () => {
      const el = editorRef.current;
      if (!el) return;
      if (serializeFromDom(el).trim().length === 0) {
        setCaretToStart(el);
      } else {
        setCaretToEnd(el);
      }
    };

    window.addEventListener("add-context-marker", handleAddMarker);
    window.addEventListener("focus-chat-input", handleFocusEvent);
    return () => {
      window.removeEventListener("add-context-marker", handleAddMarker);
      window.removeEventListener("focus-chat-input", handleFocusEvent);
    };
  }, [insertMarker]);

  // Handle Cmd+Enter to insert newline (must run before global shortcuts on window capture phase)
  useEffect(() => {
    const handleWindowKeyDown = (e: KeyboardEvent) => {
      // Only handle if this component's editor is focused
      if (document.activeElement !== editorRef.current) return;

      // Cmd+Enter (or Ctrl+Enter on non-Mac) to insert newline
      if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && !e.shiftKey) {
        e.preventDefault();
        e.stopPropagation();
        const el = editorRef.current;
        if (!el) return;
        insertPlainTextAtCursor(el, "\n");
        emitChangeFromDom();
      }
    };

    // Use window capture phase to run before document-level global shortcuts
    window.addEventListener('keydown', handleWindowKeyDown, true);
    return () => window.removeEventListener('keydown', handleWindowKeyDown, true);
  }, [emitChangeFromDom]);

  const handleKeyDown = useCallback((e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Escape" && isStreaming && onStop) {
      e.preventDefault();
      onStop();
      return;
    }

    if (e.key === "Enter" && !e.shiftKey && !e.metaKey && !e.ctrlKey && !isStreaming) {
      e.preventDefault();
      onSend();
      return;
    }

    // Handle Shift+Enter to insert a line break
    if (e.key === "Enter" && e.shiftKey) {
      // Don't preventDefault - let browser insert <br> naturally
      // Just stop propagation to prevent other handlers
      e.stopPropagation();
      
      // Mark that we're expecting a line break insertion
      // Skip normalization to preserve the <br> the browser will insert
      skipNormalizeNextRef.current = true;
      skipNextSyncRef.current = true;
      
      // Let browser's default behavior insert the <br>
      // The onInput event will fire and call emitChangeFromDom
      // which will skip normalization and update the value
      return;
    }

    // Backspace token deletion: if caret is right after a token, remove it.
    if (e.key === "Backspace") {
      const el = editorRef.current;
      const sel = window.getSelection();
      const range = sel?.rangeCount ? sel.getRangeAt(0) : null;
      if (!el || !range || !range.collapsed) return;

      const container = range.startContainer;
      const offset = range.startOffset;

      // If we're at start of a text node, check previous sibling
      if (container.nodeType === Node.TEXT_NODE && offset === 0) {
        const prev = (container as Text).previousSibling as HTMLElement | null;
        if (prev?.dataset?.marker) {
          e.preventDefault();
          prev.remove();
          emitChangeFromDom();
        }
      }
    }
  }, [isStreaming, onStop, onSend, emitChangeFromDom]);

  const handlePaste = useCallback((e: React.ClipboardEvent<HTMLDivElement>) => {
    // Let global/document listeners handle file pastes.
    if (e.clipboardData.files && e.clipboardData.files.length > 0) {
      return;
    }

    // Get plain text from clipboard, stripping rich formatting.
    const text = e.clipboardData.getData('text/plain');
    if (!text) return;

    e.preventDefault();
    e.stopPropagation();

    const el = editorRef.current;
    if (!el) return;

    // Use execCommand('insertText') to preserve the browser's native undo
    // stack so Cmd+Z works after paste.
    el.focus();
    document.execCommand('insertText', false, text);

    // Skip normalization and DOM rebuild for the input event that follows.
    // execCommand with multi-line text can produce DOM structures (e.g. <div>
    // wrappers instead of <br>) that don't round-trip cleanly through
    // serializeFromDom. We intentionally leave the DOM as-is to preserve
    // the browser's undo stack. The useLayoutEffect sync will clear
    // skipNextSyncRef and rebuild the DOM if there's a mismatch, which
    // normalizes the structure without breaking undo history.
    skipNormalizeNextRef.current = true;
    skipNextSyncRef.current = true;
  }, []);

  const handleClick = useCallback((e: React.MouseEvent<HTMLDivElement>) => {
    const target = e.target as HTMLElement;
    const removeBtn = target.closest("[data-remove-marker='true']") as HTMLElement | null;
    if (removeBtn) {
      const token = removeBtn.closest("[data-marker]") as HTMLElement | null;
      if (token) {
        token.remove();
        emitChangeFromDom();
      }
      return;
    }

    const token = target.closest("[data-marker]") as HTMLElement | null;
    if (!token) return;
    const filePath = token.dataset.filePath;
    const startLine = parseInt(token.dataset.startLine || "1", 10);
    const endLine = parseInt(token.dataset.endLine || token.dataset.startLine || "1", 10);
    if (!filePath) return;

    const parsed = parseFilePath(filePath);
    if (parsed) {
      parsed.line = startLine;
      parsed.lineEnd = endLine !== startLine ? endLine : undefined;
      parsed.column = 1;
      openFile(parsed);
    } else {
      openFile({
        fullPath: filePath,
        path: filePath,
        line: startLine,
        lineEnd: endLine !== startLine ? endLine : undefined,
        column: 1,
        isAbsolute: filePath.startsWith("/"),
      });
    }
  }, [emitChangeFromDom, openFile]);

  return (
    <div
      ref={editorRef}
      role="textbox"
      aria-multiline="true"
      contentEditable={!disabled}
      suppressContentEditableWarning
      onInput={emitChangeFromDom}
      onKeyDown={handleKeyDown}
      onPaste={handlePaste}
      onFocus={handleFocus}
      onClick={handleClick}
      data-testid="chat-input"
      data-placeholder={placeholder || defaultPlaceholder}
      data-empty={value.trim().length === 0 ? "true" : "false"}
      className="chat-composer w-full px-0 py-0 text-sm chat-input disabled:opacity-50 disabled:cursor-not-allowed transition-all duration-300 ease-in-out overflow-y-auto scroll-smooth"
      style={{
        color: "var(--chat-input-text)",
        backgroundColor: "transparent",
        maxHeight: 300,
      }}
    />
  );
});