import { type ReactNode } from "react";

interface PermissionsPanelWrapperProps {
  children: ReactNode;
}

/**
 * Wrapper for permissions panel that ensures it aligns with chat input width.
 * Uses the same max-w-[var(--chat-column)] constraint with consistent horizontal spacing.
 */
export function PermissionsPanelWrapper({ children }: PermissionsPanelWrapperProps) {
  return (
    <div className="px-[var(--chat-gutter)] flex-shrink-0">
      <div className="max-w-[var(--chat-column)] mx-auto flex justify-end">
        {children}
      </div>
    </div>
  );
}
