import type { FileChangeStatus } from "../../../gen/reliant/v1/common_pb";

export interface FileChange {
  path: string;
  status: FileChangeStatus;
  diff?: string;
  content?: string;
  original_content?: string;
  is_new: boolean;
}

export interface RecentChangesData {
  branch: string;
  files: FileChange[];
  total_files: number;
  ahead: number;
  behind: number;
  default_branch: string; // Repository's default branch (for PR targeting)
}

/** The three list sections. "modified" renders as "Changes". */
export type ChangeGroup = "staged" | "modified" | "untracked";
