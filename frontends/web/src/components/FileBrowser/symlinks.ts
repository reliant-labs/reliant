import type { FileNode } from "./index";

/**
 * Why the tree lists an entry it cannot open or expand, or null when it can.
 * Only a symbolic link the daemon refused to follow has a reason: a dangling
 * link, a link to a folder that contains it, or a link into a credential store.
 */
export function fileNodeUnavailableReason(node: FileNode): string | null {
  const target = node.symlinkTarget ? ` → ${node.symlinkTarget}` : "";
  switch (node.symlinkStatus) {
    case "broken":
      return `Broken symbolic link${target}: its target does not exist`;
    case "loop":
      return `Symbolic link${target} to a folder that contains it`;
    case "denied":
      return `Symbolic link${target} to a folder the file tree does not open`;
    default:
      return null;
  }
}

/**
 * Whether a directory row offers to expand: the backend's hint, or (for
 * eagerly-loaded data) children already present, and never a link the daemon
 * refused to follow.
 */
export function fileNodeExpandable(node: FileNode): boolean {
  if (node.type !== "directory" || fileNodeUnavailableReason(node) !== null) return false;
  return node.hasChildren ?? (node.children?.length ?? 0) > 0;
}
