/**
 * BaseChatInput - A reusable chat input component with consistent styling
 * 
 * This component provides the core chat input UI that can be customized
 * for different contexts (main chat, workflow builder, etc.)
 */

import {
  useRef,
  useCallback,
  useEffect,
  useState,
  forwardRef,
  useImperativeHandle,
  type ReactNode,
} from "react";
import { Square } from "lucide-react";
import { IoMdReturnLeft } from "react-icons/io";
import { cn } from "../../lib/utils";
import { ChatTextArea } from "./ChatTextArea";
import { ChatButton } from "./ChatButton";

export interface BaseChatInputProps {
  /** Current input value */
  value: string;
  /** Callback when input changes */
  onChange: (value: string) => void;
  /** Callback when message is sent */
  onSend: () => void;
  /** Callback when stop is requested */
  onStop?: () => void;
  /** Whether input is disabled */
  disabled?: boolean;
  /** Whether assistant is currently streaming/loading */
  isLoading?: boolean;
  /** Placeholder text */
  placeholder?: string;
  /** Maximum height for the textarea before scrolling */
  maxHeight?: number;
  /** Content to show in the bottom left (workflow selector, presets, etc.) */
  bottomLeftContent?: ReactNode;
  /** Content to show in the bottom right (additional action buttons) */
  bottomRightContent?: ReactNode;
  /** Whether to show the hint text below the input */
  showHint?: boolean;
  /** Custom hint text */
  hintText?: string;
  /** Additional class names for the container */
  className?: string;
  /** 
   * When true and there's no bottomLeftContent/bottomRightContent, 
   * show send button inline with the textarea instead of on a separate row 
   */
  inlineSendButton?: boolean;
}

export const BaseChatInput = forwardRef<HTMLDivElement, BaseChatInputProps>(
  function BaseChatInput(
    {
      value,
      onChange,
      onSend,
      onStop,
      disabled = false,
      isLoading = false,
      placeholder = "Type a message...",
      maxHeight: _maxHeight = 300,
      bottomLeftContent,
      bottomRightContent,
      showHint = true,
      hintText = "Press Enter to send, Shift+Enter for new line",
      className,
      inlineSendButton = false,
    },
    ref
  ) {
    const textareaRef = useRef<HTMLDivElement>(null);

    // Forward the internal ref to the parent
    useImperativeHandle(ref, () => textareaRef.current!, []);

    const handleSend = useCallback(() => {
      if (!value.trim() || isLoading || disabled) return;
      onSend();
    }, [value, isLoading, disabled, onSend]);



    const canSend = value.trim().length > 0 && !disabled;

    // Stable streaming state that doesn't flicker during transitions
    const [stableStreaming, setStableStreaming] = useState(isLoading);

    useEffect(() => {
      if (isLoading) {
        setStableStreaming(true);
      } else {
        const timeout = setTimeout(() => setStableStreaming(false), 100);
        return () => clearTimeout(timeout);
      }
    }, [isLoading]);

    const safeOnStop = onStop ?? (() => {});
    
    // Determine if we should show inline mode (send button next to textarea)
    const hasBottomContent = Boolean(bottomLeftContent || bottomRightContent);
    const useInlineMode = inlineSendButton && !hasBottomContent;
    
    // Render the send/stop button
    const sendButton = (
      <ChatButton
        onClick={isLoading ? safeOnStop : handleSend}
        disabled={isLoading ? false : !canSend}
        tooltip={isLoading ? "Stop generation (Esc)" : "Send message (Enter)"}
        testId={isLoading ? undefined : "send-button"}
        className={
          stableStreaming
            ? "border bg-destructive/10 text-destructive border-destructive/20 hover:bg-destructive/20"
            : "border bg-[var(--chat-button-bg)] text-[var(--chat-button-text)] border-[var(--chat-border)] hover:bg-[var(--chat-button-hover)]"
        }
      >
        <span className="inline-flex items-center justify-center" style={{ width: '46px' }}>
          {isLoading ? (
            <>
              <Square className="w-3.5 h-3.5" />
              <span className="ml-1 text-[10px]">Esc</span>
            </>
          ) : (
            <>
              <span className="text-[10px]">Send</span>
              <IoMdReturnLeft className="w-3 h-3 ml-0.5" />
            </>
          )}
        </span>
      </ChatButton>
    );

    return (
      <div className={cn("flex flex-col", className)}>
        <div
          className={cn(
            "rounded-lg border-2 border-border/70 transition-all duration-200",
            isLoading && "border-primary/30"
          )}
          style={{
            padding: "4px 8px",
            backgroundColor: isLoading
              ? "hsl(var(--primary) / 0.05)"
              : "var(--chat-input-bg)",
          }}
        >
          <div className="flex flex-col gap-1 relative">
            {/* Text Area - with optional inline send button */}
            <div className={cn(
              "flex gap-2 pt-3 px-2",
              useInlineMode ? "items-end pb-1" : "flex-col gap-1"
            )}>
              <div className="flex-1">
                <ChatTextArea
                  ref={textareaRef}
                  value={value}
                  onChange={onChange}
                  onSend={handleSend}
                  onStop={onStop}
                  disabled={disabled}
                  isStreaming={isLoading}
                  placeholder={placeholder}
                />
              </div>
              {useInlineMode && (
                <div className="flex-shrink-0 pb-0.5">
                  {sendButton}
                </div>
              )}
            </div>

            {/* Bottom Row: Controls - only shown when we have bottom content or not inline mode */}
            {!useInlineMode && (
              <div className="flex items-center justify-between pt-2 mt-2 border-t border-border/50">
                {/* Left side: Custom content (workflow selector, presets, etc.) */}
                <div className="flex items-center gap-1 flex-wrap">
                  {bottomLeftContent}
                </div>

                {/* Right side: Custom content + Send/Stop button */}
                <div className="flex items-center gap-1.5 flex-shrink-0">
                  {bottomRightContent}
                  {sendButton}
                </div>
              </div>
            )}
          </div>
        </div>

        {/* Hint text */}
        {showHint && (
          <p className="text-xs text-muted-foreground mt-2 px-1">
            {hintText}
          </p>
        )}
      </div>
    );
  }
);
