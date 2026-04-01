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
  children?: FileNode[];
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
