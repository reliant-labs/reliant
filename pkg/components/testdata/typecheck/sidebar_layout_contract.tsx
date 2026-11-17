import React, { useRef } from "react";
import SidebarLayout, { type SidebarLayoutProps } from "./sidebar_layout";
import Modal, { type ModalProps } from "./modal";
import { useModalDialog } from "./modal_dialog";

// Existing consumer surfaces remain unchanged; the shared lifecycle is additive.
const sidebar: SidebarLayoutProps = {
  brand: "Forge",
  navItems: [{ label: "Home", href: "/", active: true }],
  children: "Page",
  collapsed: false,
  onToggle: () => {},
  contentLayout: "fill",
};
const modal: ModalProps = {
  open: false,
  onClose: () => {},
  title: "Title",
  description: "Description",
  portal: false,
  fallbackFocus: () => {},
  children: "Body",
};
export function Contract() {
  const panelRef = useRef<HTMLElement>(null);
  useModalDialog({ open: false, panelRef, onClose: () => {} });
  return (
    <>
      <SidebarLayout {...sidebar} />
      <Modal {...modal} />
    </>
  );
}
