import { type ReactNode } from "react";
import Tooltip from "../forge-ui/tooltip";
import { COMPOSER_CONTROL } from "./composerControl";

interface ChatButtonProps {
  onClick: () => void;
  disabled?: boolean;
  tooltip: string;
  children: ReactNode;
  className?: string;
  testId?: string;
  compact?: boolean; // When true, reduce padding and spacing
}

export function ChatButton({
  onClick,
  disabled = false,
  tooltip,
  children,
  className = "",
  testId,
  compact = false,
}: ChatButtonProps) {
  return (
    <Tooltip content={tooltip} side="top" delay={300} triggerClassName="inline-flex">
      <button
        type="button"
        onClick={onClick}
        disabled={disabled}
        aria-label={tooltip}
        className={`chat-button flex items-center justify-center rounded-full text-2xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed ${COMPOSER_CONTROL} ${
          compact ? "p-1 w-[32px]" : "p-1 w-[32px] gap-1"
        } touch:w-[44px] ${className}`}
        data-testid={testId}
      >
        {children}
      </button>
    </Tooltip>
  );
}
