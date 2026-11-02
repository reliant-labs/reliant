import type { ReactNode } from "react";
import Modal from "../forge-ui/modal";

/** Feature sheet: focus, Escape, restoration and portalling belong to Forge. */
export function ComposerSheet({ title, onClose, children }: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  return <Modal open title={title} onClose={onClose} size="md" sheetOnMobile className="composer-sheet">
    <div className="min-w-0 space-y-3 pb-4">{children}</div>
  </Modal>;
}
