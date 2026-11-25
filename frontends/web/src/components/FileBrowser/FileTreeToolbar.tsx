import { useRef } from "react";
import { FilePlus, FolderPlus, RefreshCw, ChevronsDownUp, Upload } from "lucide-react";
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
  /** Files the user picked to upload; absent hides the Upload button. */
  onUploadFiles?: (files: File[]) => void;
}

export function FileTreeToolbar({
  projectName,
  onNewFile,
  onNewFolder,
  onRefresh,
  onCollapseAll,
  onUploadFiles,
}: FileTreeToolbarProps) {
  const uploadInputRef = useRef<HTMLInputElement>(null);

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
          {onUploadFiles && (
            <>
              <SidebarHeaderButton
                icon={<Upload className="w-4 h-4" />}
                onClick={() => uploadInputRef.current?.click()}
                tooltip="Upload Files"
              />
              <input
                ref={uploadInputRef}
                type="file"
                multiple
                hidden
                aria-hidden="true"
                tabIndex={-1}
                data-testid="toolbar-upload-input"
                onChange={(e) => {
                  const files = Array.from(e.target.files ?? []);
                  // Clear it, so picking the same file again still fires.
                  e.target.value = "";
                  if (files.length > 0) onUploadFiles(files);
                }}
              />
            </>
          )}
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
