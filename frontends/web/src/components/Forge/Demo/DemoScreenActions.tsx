// Copyright (c) 2025 Reliant Labs

/** Persistent screen actions, separate from page explanations and data/header controls. */
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "@tanstack/react-router";
import Button from "@/components/forge-ui/button";
import { DemoHowBuiltLink } from "./DemoHowBuiltLink";
import { SignUpButton, InstallForgeLink } from "./DemoCta";
import { useConsoleDrawerOpen } from "./DemoCorner";


export function DemoScreenActions() {
  const [host, setHost] = useState<HTMLElement | null>(null);
  const drawerOpen = useConsoleDrawerOpen();
  const navigate = useNavigate();
  useEffect(() => {
    const slot = document.createElement("div");
    slot.className = "w-full shrink-0";
    const attach = () => {
      const main = document.querySelector("main");
      // A dedicated row below the existing header, outside its controls and
      // above the retained scroll host. Never overlay data or the mobile drawer.
      if (main?.parentElement && slot.parentElement !== main.parentElement) {
        main.parentElement.insertBefore(slot, main);
        setHost(slot);
      }
    };
    attach(); const observer = new MutationObserver(attach);
    observer.observe(document.body, {childList:true,subtree:true});
    return () => { observer.disconnect();slot.remove(); };
  }, []);
  if (!host || drawerOpen) return null;
  return createPortal(
    <nav aria-label="Demo actions" data-testid="demo-screen-actions" className="relative z-[101] flex flex-wrap items-center gap-2 border-b border-border bg-surface px-3 py-2 text-sm">
      <DemoHowBuiltLink placement="banner" />
      <Button variant="ghost" className="min-h-[44px]" data-testid="demo-start-tour" onClick={() => {
        void navigate({ to: "/forge/demo", search: { view: "overview", tour: "forge:fg-intro" } } as never);
      }}>Start tour</Button>
      <div className="ml-auto flex items-center gap-3">
        <InstallForgeLink placement="banner">Install Forge</InstallForgeLink>
        <SignUpButton placement="banner" size="md" />
      </div>
    </nav>,host,
  );
}
