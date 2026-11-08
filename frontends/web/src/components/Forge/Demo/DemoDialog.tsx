// Copyright (c) 2025 Reliant Labs
import { createPortal } from "react-dom";
import Modal, { type ModalProps } from "@/components/forge-ui/modal";

/** Demo chrome stacks above page callouts; keep the shared dialog above both. */
export function DemoDialog(props: ModalProps) {
  if (!props.open || typeof document === "undefined") return null;
  return createPortal(<div className="forge-ui relative z-[210]"><Modal {...props} portal={false} hideCloseButton closeOnOverlay={false} /></div>, document.body);
}
