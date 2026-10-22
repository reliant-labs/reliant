import { type ReactNode } from "react";

interface PermissionsPanelWrapperProps {
  children: ReactNode;
}

/**
 * Wrapper for permissions panel that ensures it aligns with chat input width.
 * Uses the same max-w-[1200px] constraint with consistent horizontal spacing.
 */
export function PermissionsPanelWrapper({ children }: PermissionsPanelWrapperProps) {
  return (
    <div className="px-4 sm:px-6 lg:px-8 flex-shrink-0">
      <div className="max-w-[1200px] mx-auto flex justify-end">
        {children}
      </div>
    </div>
  );
}
