import { type ReactNode } from "react";
import { Tooltip } from "../ui/Tooltip";

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
    <Tooltip content={tooltip} placement="top">
      <button
        onClick={onClick}
        disabled={disabled}
        className={`chat-button flex items-center justify-center rounded-full text-2xs font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed ${
          compact
            ? "p-1 h-6 w-6"
            : "p-1 h-6 w-6 gap-1"
        } ${className}`}
        data-testid={testId}
      >
        {children}
      </button>
    </Tooltip>
  );
}
