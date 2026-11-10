import { useState, type ReactNode } from "react";
import { dialogOpener } from "../../lib/dialogOpener";
import Modal from "../forge-ui/modal";

/**
 * Feature sheet: focus, Escape, restoration and portalling belong to Forge.
 * Safari never focuses a button on tap, so Forge's Modal finds <body> focused
 * when it opens and has no opener to restore. The tapped control is read while
 * this sheet renders, in the tap's task, and the Modal's `fallbackFocus` (what
 * it calls when there is no opener to restore) focuses it. Focusing it from an
 * effect here is too late: the Modal records its opener in a layout effect,
 * which runs before any effect of this wrapper.
 */
export function ComposerSheet({ title, onClose, children }: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const [opener] = useState(dialogOpener);
  return <Modal open title={title} onClose={onClose} fallbackFocus={() => opener?.focus()} size="md" sheetOnMobile className="composer-sheet">
    <div className="min-w-0 space-y-3 pb-4">{children}</div>
  </Modal>;
}
