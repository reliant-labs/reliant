import type { ReactNode } from "react";
import { ComposerSheet } from "./ComposerSheet";

export function ComposerOptions({ mobile, open, onClose, children }: {
  mobile: boolean; open: boolean; onClose: () => void; children: ReactNode;
}) {
  if (!mobile) return <>{children}</>;
  if (!open) return null;
  return <ComposerSheet title="Chat options" onClose={onClose}>{children}</ComposerSheet>;
}
