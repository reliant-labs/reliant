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
import { useSurface } from "../../lib/surfaceContext";
import { useVisualViewport } from "../../hooks/useVisualViewport";
import { composerMaxHeight } from "./composerHeight";
import { takeSwitchFocus } from "./composerSwitchFocus";
import { useCoarsePointer } from "./useCoarsePointer";
import {
  SlashCommandMenu,
  type SlashCommand,
  type SlashCommandMenuHandle,
} from "./SlashCommandMenu";
import "./chat-composer.css";

/**
 * The chat composer.
 *
 * This is a plain <textarea>. It used to be a contentEditable div so that file
 * context references could render as inline chips, which meant hand-maintaining
 * a DOM serializer, a normalizer for browser-inserted wrappers, and caret
 * bookkeeping — and newlines depended on the browser's default editing action
 * rather than on anything this component did. When the caret landed between
 * element children (which is where a paste leaves it), that default action did
 * nothing at all, so Shift+Enter silently stopped inserting line breaks.
 *
 * Context references now live above the text as chips instead of inside it, so
 * the text is only ever text and a textarea can hold it. Nothing is lost by
 * moving them: the send path already strips markers out of the message body and
 * appends the file contents at the end, so their inline position was never
 * meaningful.
 */

interface ChatTextAreaProps {
  value: string;
  onChange: (value: string) => void;
  onSend: () => void;
  onStop?: () => void;
  /**
   * Called instead of `onSend` when the send key is pressed while streaming.
   * Queues the message for the agent's next turn. The send key is always
   * handled deliberately while streaming -- if this is omitted, it falls
   * back to `onStop` (matching the "streaming turns the send button into
   * stop" behavior elsewhere) rather than inserting a newline or doing
   * nothing.
   */
  onQueue?: () => void;
  disabled?: boolean;
  isStreaming?: boolean;
  placeholder?: string;
  chatId?: string;
  /**
   * Actions offered by the "/" menu. Omit to disable slash commands (e.g. in
   * composers where a leading slash is ordinary text).
   */
  slashCommands?: SlashCommand[];
}

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

/** Focus the composer with the caret after any draft, ready to keep typing. */
function focusAtEnd(el: HTMLTextAreaElement): void {
  el.focus();
  el.setSelectionRange(el.value.length, el.value.length);
}

export const ChatTextArea = forwardRef<HTMLTextAreaElement, ChatTextAreaProps>(
  function ChatTextArea(
    {
      value,
      onChange,
      onSend,
      onStop,
      onQueue,
      disabled = false,
      isStreaming = false,
      placeholder,
      chatId,
      slashCommands,
    },
    ref,
  ) {
    const textareaRef = useRef<HTMLTextAreaElement>(null);
    const slashMenuRef = useRef<SlashCommandMenuHandle>(null);

    // Expose the internal ref to the parent
    useImperativeHandle(ref, () => textareaRef.current!, []);
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

    // On the phone the cap follows the visible viewport, so an open keyboard
    // cannot let a long message push the timeline off screen.
    const isMobile = useSurface() === "mobile";
    const viewportHeight = useVisualViewport(isMobile)?.height;

    /** Grow to fit the content, up to the cap, then scroll inside. */
    const adjustHeight = useCallback(() => {
      const el = textareaRef.current;
      if (!el) return;

      const style = window.getComputedStyle(el);
      const lineHeight = parseFloat(style.lineHeight) || 20;
      const paddingTop = parseFloat(style.paddingTop) || 0;
      const paddingBottom = parseFloat(style.paddingBottom) || 0;
      const paddingY = paddingTop + paddingBottom;
      const minHeight = lineHeight + paddingY;
      const maxHeight = composerMaxHeight({
        lineHeight,
        paddingY,
        viewportHeight: isMobile ? viewportHeight : undefined,
      });

      // Collapsing to `auto` below clamps scrollTop to 0; put it back so a
      // capped composer does not jump to its first line on every keystroke.
      const scrollTop = el.scrollTop;
      // scrollHeight only reports the content height once the element is not
      // already sized to fit it.
      el.style.height = "auto";
      el.style.maxHeight = `${maxHeight}px`;
      el.style.height = `${Math.min(Math.max(el.scrollHeight, minHeight), maxHeight)}px`;

      const caretAtEnd =
        document.activeElement === el &&
        el.selectionStart === el.selectionEnd &&
        el.selectionEnd === el.value.length;
      el.scrollTop = caretAtEnd ? el.scrollHeight : scrollTop;
    }, [isMobile, viewportHeight]);

    useLayoutEffect(() => {
      adjustHeight();
    }, [value, adjustHeight]);

    const handleFocus = useCallback(() => {
      // When text input is focused, just ensure chat is active.
      // Don't clear viewer - allow both to remain visible.
      if (activeChatId) {
        useChatNavigationStore.getState().navigateToChat(activeChatId);
      }
    }, [activeChatId]);

    const handleChange = useCallback(
      (e: React.ChangeEvent<HTMLTextAreaElement>) => {
        onChange(e.target.value);
      },
      [onChange],
    );

    // What the send key does: send, queue into the running agent, or (with no
    // mailbox to queue into, e.g. the workflow-builder composer) stop --
    // matching what the action button already does once streaming starts.
    const submit = useCallback(() => {
      if (!isStreaming) {
        onSend();
      } else if (onQueue) {
        onQueue();
      } else if (onStop) {
        onStop();
      }
    }, [isStreaming, onSend, onQueue, onStop]);

    // Which key sends depends on the keyboard (design doc §5.3, D2). With a
    // fine pointer -- a desktop keyboard -- Enter sends and Cmd+Enter inserts
    // a newline. With a coarse pointer the on-screen keyboard's Return is the
    // only way to break a line, and a stray tap on it must not send a
    // half-typed prompt, so plain Enter falls through to the newline and only
    // the action button or Cmd/Ctrl+Enter (an iPad's hardware keyboard) sends.
    const coarsePointer = useCoarsePointer();

    const handleKeyDown = useCallback(
      (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
        // The slash menu gets first refusal: while it is open, Arrow/Enter/Escape
        // belong to it, not to send/stop. It returns false for anything it does
        // not use, so ordinary typing falls straight through.
        if (slashMenuRef.current?.handleKeyDown(e)) {
          return;
        }

        if (e.key === "Escape" && isStreaming && onStop) {
          e.preventDefault();
          onStop();
          return;
        }

        // A composition in progress owns Enter — committing a candidate must
        // not send the message.
        if (e.nativeEvent.isComposing) return;

        if (e.key !== "Enter" || e.shiftKey) return; // Shift+Enter: the textarea's own newline.
        const modified = e.metaKey || e.ctrlKey;

        if (coarsePointer) {
          // Plain Return: the textarea's newline. Cmd/Ctrl+Enter normally
          // reaches the shortcut dispatcher first (capture phase), which routes
          // it to the "composer-insert-newline" listener below; this branch is
          // the path where no dispatcher claimed it.
          if (!modified || e.defaultPrevented) return;
          e.preventDefault();
          submit();
          return;
        }

        // Fine pointer: Cmd+Enter belongs to the newline shortcut.
        if (modified) return;
        // Enter is fully handled here -- it must never fall through to the
        // textarea's default newline insertion. A keystroke must always
        // produce a deliberate outcome.
        e.preventDefault();
        submit();
      },
      [isStreaming, onStop, coarsePointer, submit],
    );

    // Focus request from elsewhere in the app (e.g. after attaching a file).
    useEffect(() => {
      const handleFocusEvent = () => {
        const el = textareaRef.current;
        if (el) focusAtEnd(el);
      };

      window.addEventListener("focus-chat-input", handleFocusEvent);
      return () =>
        window.removeEventListener("focus-chat-input", handleFocusEvent);
    }, []);

    // Switching to this chat puts the cursor here, once the composer is
    // mounted and enabled. The decision (and its guards) lives in
    // composerSwitchFocus; re-checked when the active chat changes in case
    // this composer mounted before its chat became active.
    useEffect(() => {
      const el = textareaRef.current;
      if (!chatId || disabled || !el) return;
      if (takeSwitchFocus(chatId)) focusAtEnd(el);
    }, [chatId, activeChatId, disabled]);

    // Cmd+Enter inserts a newline while the composer has focus -- with a fine
    // pointer. With a coarse one Return already inserts newlines, so the
    // chord is the hardware keyboard's send key instead (see handleKeyDown).
    //
    // Registered as a shortcut in the `chat-input` context rather than a window
    // capture-phase listener: a listener that ran before the dispatcher and
    // stopped propagation silently killed the global Cmd+Enter ("approve tool
    // requests") whenever the composer was focused.
    useEffect(() => {
      const handleNewline = () => {
        const el = textareaRef.current;
        // The dispatcher resolved `chat-input`, but that context covers the
        // whole composer subtree — confirm this textarea is the focused one.
        if (!el || document.activeElement !== el) return;

        if (coarsePointer) {
          submit();
          return;
        }

        const { selectionStart, selectionEnd } = el;
        const next =
          value.slice(0, selectionStart) + "\n" + value.slice(selectionEnd);
        onChange(next);

        // Restore the caret after the inserted newline once React has
        // re-rendered with the new value.
        requestAnimationFrame(() => {
          const caret = selectionStart + 1;
          el.setSelectionRange(caret, caret);
        });
      };

      window.addEventListener("composer-insert-newline", handleNewline);
      return () =>
        window.removeEventListener("composer-insert-newline", handleNewline);
    }, [value, onChange, coarsePointer, submit]);

    // Clear the composer before a slash command runs, so the "/whatever" text is
    // not left behind as a message the user never meant to send.
    const clearComposer = useCallback(() => {
      onChange("");
    }, [onChange]);

    // Cmd+/ opens the menu without typing a slash, so it works mid-sentence.
    // Routed through events because the shortcut is dispatched globally while
    // the menu state lives here.
    useEffect(() => {
      const handleOpen = () => slashMenuRef.current?.open();
      const handleDismiss = () => slashMenuRef.current?.dismiss();

      window.addEventListener("open-slash-menu", handleOpen);
      window.addEventListener("dismiss-slash-menu", handleDismiss);
      return () => {
        window.removeEventListener("open-slash-menu", handleOpen);
        window.removeEventListener("dismiss-slash-menu", handleDismiss);
      };
    }, []);

    return (
      <>
        <textarea
          aria-label="Message the agent"
          ref={textareaRef}
          rows={1}
          value={value}
          disabled={disabled}
          onChange={handleChange}
          onKeyDown={handleKeyDown}
          onFocus={handleFocus}
          data-testid="chat-input"
          data-context="chat-input"
          placeholder={placeholder || defaultPlaceholder}
          // text-base, not text-sm: this is the app's primary input, and it was
          // rendering a step below body text. It scales with the Appearance
          // font-size preference like everything else. The one-line box is
          // padded to the height of the controls beside it (32px, 44px on
          // touch), so the text line sits level with ⊕ and send, and a tap
          // just above or below the line still lands in the field (the
          // auto-resize counts the padding).
          className="chat-composer w-full resize-none border-0 bg-transparent px-0 py-1 text-base chat-input outline-none disabled:cursor-not-allowed disabled:opacity-50 touch:min-h-[44px] touch:py-2.5"
          style={{
            color: "var(--chat-input-text)",
          }}
        />
        {slashCommands && slashCommands.length > 0 && (
          <SlashCommandMenu
            ref={slashMenuRef}
            value={value}
            commands={slashCommands}
            anchorRef={textareaRef}
            onConsume={clearComposer}
          />
        )}
      </>
    );
  },
);
