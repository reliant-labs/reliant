import { useLayoutEffect, type ReactNode } from "react";
import { focusDialogOpener } from "../../lib/dialogOpener";
import Modal from "../forge-ui/modal";

/**
 * Feature sheet: focus, Escape, restoration and portalling belong to Forge.
 * Safari never focuses a button on tap, so Forge's Modal would record <body>
 * as the opener; focusing the tapped control first makes it restore there.
 */
export function ComposerSheet({ title, onClose, children }: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  useLayoutEffect(focusDialogOpener, []);
  return <Modal open title={title} onClose={onClose} size="md" sheetOnMobile className="composer-sheet">
    <div className="min-w-0 space-y-3 pb-4">{children}</div>
  </Modal>;
}
