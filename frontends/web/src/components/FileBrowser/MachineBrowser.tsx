// Copyright (c) 2025 Reliant Labs

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { ArrowUp, Copy, CornerDownLeft, Download, FileText, Folder, FolderOpen, Home, Link2, Loader2, RefreshCw } from "lucide-react";

import { listMachineDirectory, type MachineDirectoryEntry, type MachineDirectoryListing } from "../../api/fileSystem";
import { cn } from "../../lib/utils";
import { hasErrorReason } from "../../lib/errorReasons";
import { isDaemonConnectingError } from "../../lib/daemon-errors";
import { retryAcrossWake } from "../../lib/daemon-retry";
import { contextMenuOpening, menuGroups, MENU_SEPARATOR, type ContextMenuOpening } from "../../lib/contextMenuOpening";
import { toast } from "../../lib/toast-manager";
import { useProjectStore } from "../../store/projectStore";
import { useViewerStore } from "../../store/viewerStore";
import { chatScratchPath, parentPath, pathSegments, useFileBrowserScopeStore } from "../../store/fileBrowserScopeStore";
import ContextMenu from "../forge-ui/context_menu";
import type { MenuItem } from "../forge-ui/dropdown_menu";
import Tooltip from "../forge-ui/tooltip";
import { FileIcon } from "./FileIcon";
import { downloadWorkspaceFile } from "./downloadWorkspaceFile";
import type { FileNode } from "./index";

interface MachineBrowserProps {
  /** Which machine-wide scope this is: the whole machine, or the chat's scratch directory. */
  scope: "machine" | "scratch";
  worktreeId?: string;
  chatId?: string | null;
  showHidden: boolean;
}

/** What a failed listing tells the user, and whether retrying can help. */
export function machineListErrorMessage(error: unknown, scope: "machine" | "scratch", atRoot: boolean): { title: string; retry: boolean } {
  if (hasErrorReason(error, "machine_browse_unsupported")) {
    return { title: "Update Reliant on this machine to browse the whole machine.", retry: false };
  }
  if (error instanceof ConnectError) {
    switch (error.code) {
      case Code.Unimplemented:
        return { title: "This Reliant server can't browse the machine yet.", retry: false };
      case Code.NotFound:
        return scope === "scratch" && atRoot
          ? { title: "This chat hasn't written any scratch files yet.", retry: true }
          : { title: "That folder doesn't exist on this machine.", retry: true };
      case Code.PermissionDenied:
        return { title: error.rawMessage || "You can't open that folder.", retry: false };
    }
    if (isDaemonConnectingError(error)) {
      return { title: "Waiting for the machine to connect…", retry: true };
    }
    return { title: error.rawMessage, retry: true };
  }
  return { title: error instanceof Error ? error.message : "Couldn't list this folder.", retry: true };
}

function sortEntries(entries: MachineDirectoryEntry[]): MachineDirectoryEntry[] {
  return [...entries].sort((a, b) => {
    if (a.isDirectory !== b.isDirectory) return a.isDirectory ? -1 : 1;
    return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
  });
}

/**
 * The Files tab's machine-wide view: one directory of the machine that holds
 * the workspace at a time, with a breadcrumb that reaches "/", Up and Home,
 * and a "Go to path…" box that takes any path an agent mentioned (a file
 * opens in the viewer). Read-only by design: a file opens in the viewer or
 * downloads; nothing here writes outside the project.
 */
export function MachineBrowser({ scope, worktreeId, chatId, showHidden }: MachineBrowserProps) {
  const projectId = useProjectStore((s) => s.currentProject?.id);
  const openFileViewer = useViewerStore((s) => s.openFileViewer);
  const storedPath = useFileBrowserScopeStore((s) => s.paths[scope]);
  const highlight = useFileBrowserScopeStore((s) => s.highlight);
  const navigate = useFileBrowserScopeStore((s) => s.navigate);

  const rootPath = scope === "scratch" ? (chatId ? chatScratchPath(chatId) : null) : "";
  const requestedPath = storedPath ?? rootPath;
  const atRoot = storedPath === null;

  const query = useQuery<MachineDirectoryListing, Error>({
    queryKey: ["machineDirectory", projectId, worktreeId ?? null, chatId ?? null, requestedPath],
    queryFn: () => retryAcrossWake(() => listMachineDirectory(requestedPath ?? "", worktreeId)),
    enabled: !!projectId && requestedPath !== null,
    retry: false,
    staleTime: 5_000,
  });
  const listing = query.data;

  const [goTo, setGoTo] = useState("");
  const [goToBusy, setGoToBusy] = useState(false);
  const [contextMenu, setContextMenu] = useState<(ContextMenuOpening & { entry: MachineDirectoryEntry }) | null>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const openFile = useCallback(
    (path: string, name: string) => {
      if (!projectId) return;
      const node: FileNode = { name, path, type: "file" };
      openFileViewer(node, projectId, worktreeId, false);
    },
    [projectId, worktreeId, openFileViewer],
  );

  const go = useCallback((path: string | null) => navigate(scope, path), [navigate, scope]);

  // "Go to path…": a folder is shown here; a file opens in the viewer, and the
  // view moves to the folder that holds it.
  const submitGoTo = useCallback(async () => {
    const target = goTo.trim();
    if (!target) return;
    setGoToBusy(true);
    try {
      const result = await retryAcrossWake(() => listMachineDirectory(target, worktreeId));
      go(result.path);
      setGoTo("");
    } catch (error) {
      if (hasErrorReason(error, "not_a_directory")) {
        const name = target.split("/").filter(Boolean).pop() ?? target;
        openFile(target, name);
        if (target.startsWith("/")) go(parentPath(target));
        setGoTo("");
      } else {
        toast.warning(`Couldn't open ${target}: ${machineListErrorMessage(error, scope, false).title}`);
      }
    } finally {
      setGoToBusy(false);
    }
  }, [goTo, worktreeId, go, openFile, scope]);

  const entries = useMemo(
    () => sortEntries((listing?.entries ?? []).filter((e) => showHidden || !e.name.startsWith("."))),
    [listing, showHidden],
  );

  // A reveal points at one entry: scroll it into view once it is listed.
  useEffect(() => {
    if (!highlight || !listing) return;
    const el = listRef.current?.querySelector(`[data-machine-path="${CSS.escape(highlight)}"]`);
    el?.scrollIntoView({ block: "nearest" });
  }, [highlight, listing]);

  // The entry a reveal pointed at, else the workspace's own folder ("you are
  // here" when the view climbed above the project).
  const isHighlighted = (e: MachineDirectoryEntry) =>
    highlight ? e.path === highlight || e.name === highlight : !!listing?.workspaceRoot && e.path === listing.workspaceRoot;

  const menuItems = (entry: MachineDirectoryEntry): Array<MenuItem | typeof MENU_SEPARATOR> => [
    entry.isDirectory
      ? { label: "Open", icon: <FolderOpen className="w-4 h-4" />, onClick: () => go(entry.path) }
      : { label: "Open", icon: <FileText className="w-4 h-4" />, onClick: () => openFile(entry.path, entry.name) },
    ...(entry.isDirectory
      ? []
      : [{ label: "Download", icon: <Download className="w-4 h-4" />, onClick: () => void downloadWorkspaceFile(entry.path, entry.name, worktreeId) }]),
    MENU_SEPARATOR,
    {
      label: "Copy Path",
      icon: <Copy className="w-4 h-4" />,
      onClick: () => {
        void navigator.clipboard?.writeText(entry.path);
        toast.notify("Path copied", { description: entry.path });
      },
    },
  ];

  if (scope === "scratch" && !chatId) {
    return <Empty title="Open a chat to see its scratch directory." />;
  }

  const shownPath = listing?.path ?? "";
  const error = query.error;
  const message = error ? machineListErrorMessage(error, scope, atRoot) : null;

  return (
    <div className="flex h-full flex-col min-h-0" data-testid={`machine-browser-${scope}`}>
      <div className="flex items-center gap-1 px-2 pt-2">
        <IconButton label="Up to the parent folder" disabled={!shownPath || shownPath === "/"} onClick={() => go(parentPath(shownPath))}>
          <ArrowUp className="w-4 h-4" />
        </IconButton>
        <IconButton label={scope === "scratch" ? "This chat's scratch directory" : "Home folder"} onClick={() => go(null)}>
          <Home className="w-4 h-4" />
        </IconButton>
        <IconButton label="Refresh" onClick={() => void query.refetch()}>
          <RefreshCw className={cn("w-4 h-4", query.isFetching && "animate-spin")} />
        </IconButton>
        <form
          className="relative flex-1 min-w-0"
          onSubmit={(e) => {
            e.preventDefault();
            void submitGoTo();
          }}
        >
          <input
            value={goTo}
            onChange={(e) => setGoTo(e.target.value)}
            placeholder="Go to path…"
            aria-label="Go to path"
            spellCheck={false}
            className="w-full rounded-md border border-border bg-background px-2 py-1 pr-7 text-ui font-mono placeholder:font-sans placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
          <button
            type="submit"
            aria-label="Go"
            disabled={!goTo.trim() || goToBusy}
            className="absolute right-1 top-1/2 -translate-y-1/2 rounded-md p-0.5 text-muted-foreground hover:bg-foreground/5 hover:text-foreground disabled:opacity-40"
          >
            {goToBusy ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <CornerDownLeft className="w-3.5 h-3.5" />}
          </button>
        </form>
      </div>

      {shownPath && (
        <nav aria-label="Folder path" className="flex flex-wrap items-center gap-0.5 px-3 py-1.5 text-meta font-mono text-muted-foreground">
          {pathSegments(shownPath).map((seg, i, all) => (
            <span key={seg.path} className="flex items-center gap-0.5 min-w-0">
              {i > 1 && <span aria-hidden="true">/</span>}
              <button
                type="button"
                onClick={() => go(seg.path)}
                aria-current={i === all.length - 1 ? "location" : undefined}
                className={cn("truncate rounded-md px-0.5 hover:bg-foreground/5 hover:text-foreground", i === all.length - 1 && "text-foreground")}
              >
                {seg.name}
              </button>
            </span>
          ))}
        </nav>
      )}

      <div ref={listRef} className="flex-1 overflow-y-auto pb-2" aria-label="Folder contents">
        {query.isLoading && (
          <div className="flex items-center gap-2 px-3 py-4 text-ui text-muted-foreground">
            <Loader2 className="w-4 h-4 animate-spin" /> Loading…
          </div>
        )}
        {message && (
          <Empty title={message.title}>
            <div className="mt-3 flex items-center justify-center gap-2">
              {message.retry && (
                <button type="button" onClick={() => void query.refetch()} className="rounded-md px-2 py-1 text-ui text-primary-ink hover:bg-foreground/5">
                  Retry
                </button>
              )}
              {!atRoot && (
                <button type="button" onClick={() => go(null)} className="rounded-md px-2 py-1 text-ui text-primary-ink hover:bg-foreground/5">
                  {scope === "scratch" ? "Back to scratch" : "Go home"}
                </button>
              )}
            </div>
          </Empty>
        )}
        {listing && !error && entries.length === 0 && <Empty title="This folder is empty." />}
        {!error &&
          entries.map((entry) => (
            <button
              key={entry.path}
              type="button"
              data-machine-path={entry.path}
              title={entry.path}
              onClick={() => (entry.isDirectory ? go(entry.path) : openFile(entry.path, entry.name))}
              onContextMenu={(e) => {
                e.preventDefault();
                setContextMenu({ ...contextMenuOpening(e), entry });
              }}
              className={cn(
                "flex w-full items-center gap-2 px-3 py-1 text-left text-ui text-foreground hover:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                isHighlighted(entry) && "bg-foreground/5",
              )}
            >
              {entry.isDirectory ? (
                <Folder className="w-4 h-4 shrink-0 text-muted-foreground" />
              ) : (
                <FileIcon fileName={entry.name} className="w-4 h-4 shrink-0" />
              )}
              <span className="truncate">{entry.name}</span>
              {entry.isSymlink && <Link2 aria-label="symbolic link" className="w-3 h-3 shrink-0 text-muted-foreground" />}
            </button>
          ))}
        {listing?.truncated && !error && (
          <p className="px-3 py-2 text-meta text-muted-foreground">Showing the first {listing.entries.length} entries.</p>
        )}
      </div>

      {contextMenu && (
        <ContextMenu
          label={`${contextMenu.entry.name} actions`}
          groups={menuGroups(menuItems(contextMenu.entry))}
          open
          position={contextMenu}
          openVia={contextMenu.via}
          onOpenChange={(open) => {
            if (!open) setContextMenu(null);
          }}
        />
      )}
    </div>
  );
}

function IconButton({ label, onClick, disabled, children }: { label: string; onClick: () => void; disabled?: boolean; children: React.ReactNode }) {
  return (
    <Tooltip content={label} side="bottom" delay={300} triggerClassName="inline-flex">
      <button
        type="button"
        aria-label={label}
        onClick={onClick}
        disabled={disabled}
        className="rounded-md p-1 text-muted-foreground hover:bg-foreground/5 hover:text-foreground disabled:opacity-40 disabled:hover:bg-transparent"
      >
        {children}
      </button>
    </Tooltip>
  );
}

function Empty({ title, children }: { title: string; children?: React.ReactNode }) {
  return (
    <div className="px-4 py-6 text-center">
      <p className="text-ui text-muted-foreground">{title}</p>
      {children}
    </div>
  );
}
