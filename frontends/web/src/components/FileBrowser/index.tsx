// Type definitions
export interface FileDiagnostic {
  severity: "error" | "warning" | "info" | "hint";
  message: string;
  line?: number;
  column?: number;
}

export interface FileNode {
  name: string;
  path: string;
  type: "file" | "directory";
  // children === undefined means "not yet loaded" (a lazily-loaded directory at
  // a depth boundary); [] means "loaded and empty". Files never have children.
  children?: FileNode[];
  // hasChildren is a directory hint from the backend: true when the directory
  // has at least one entry. Used to render an expand chevron without fetching.
  hasChildren?: boolean;
  size?: number;
  modified?: string;
  diagnostics?: FileDiagnostic[];
  errorCount?: number;
  warningCount?: number;
  // Optional line navigation for opening files at specific positions
  line?: number;
  lineEnd?: number;
  column?: number;
}

// Re-export components
export { RightSidebar } from "./RightSidebar";
export { FileViewerTab } from "./FileViewerTab";
export { GlobalSearch } from "./GlobalSearch";
export { FindReplace } from "./FindReplace";
