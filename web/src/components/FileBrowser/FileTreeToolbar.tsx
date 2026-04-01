import { FilePlus, FolderPlus, RefreshCw, ChevronsDownUp } from "lucide-react";
import {
  SidebarHeader,
  SidebarHeaderButton,
} from "../RightSidebar/shared";

interface FileTreeToolbarProps {
  projectName?: string | null;
  onNewFile: () => void;
  onNewFolder: () => void;
  onRefresh: () => void;
  onCollapseAll: () => void;
}

export function FileTreeToolbar({
  projectName,
  onNewFile,
  onNewFolder,
  onRefresh,
  onCollapseAll,
}: FileTreeToolbarProps) {
  return (
    <SidebarHeader
      title={projectName ?? undefined}
      actions={
        <>
          <SidebarHeaderButton
            icon={<FilePlus className="w-4 h-4" />}
            onClick={onNewFile}
            tooltip="New File"
          />
          <SidebarHeaderButton
            icon={<FolderPlus className="w-4 h-4" />}
            onClick={onNewFolder}
            tooltip="New Folder"
          />
          <SidebarHeaderButton
            icon={<RefreshCw className="w-4 h-4" />}
            onClick={onRefresh}
            tooltip="Refresh"
          />
          <SidebarHeaderButton
            icon={<ChevronsDownUp className="w-4 h-4" />}
            onClick={onCollapseAll}
            tooltip="Collapse All"
          />
        </>
      }
    />
  );
}
